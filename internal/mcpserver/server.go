package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/gateway"
	"github.com/haha-systems/ariadne/internal/memory"
	"github.com/haha-systems/ariadne/internal/operator"
	"github.com/haha-systems/ariadne/internal/runstate"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	resourceOverview = "ariadne://overview"
	resourceRuns     = "ariadne://runs"
	resourceMemory   = "ariadne://memory"
)

type Config struct {
	RepoRoot        string
	WorktreeDir     string
	RunStatePath    string
	MemoryStorePath string
	Skills          map[string]config.SkillConfig
	ListenAddress   string
	MCPPath         string
	// Operator is the legacy control plane service (internal/operator).
	// It is retained only for backward compatibility. The preferred path is
	// Gateway (see mcp.go which never populates Operator anymore).
	// The fallback logic in startRun/cancelRun below will be removable once
	// all call sites are known to supply Gateway.
	Operator *operator.Service // legacy for transition / fallback only
	Gateway  gateway.Gateway   // preferred new path for direct runs (MCP as adapter)
}

type Options struct {
	LogOutput io.Writer
	Now       func() time.Time
}

type Server struct {
	cfg         Config
	logger      *slog.Logger
	now         func() time.Time
	store       *runstate.Store
	memoryStore *memory.Store
	baseURL     string
	mcpPath     string
}

type overview struct {
	Service            string   `json:"service"`
	BaseURL            string   `json:"base_url"`
	RepoRoot           string   `json:"repo_root"`
	WorktreeDir        string   `json:"worktree_dir"`
	RunStatePath       string   `json:"run_state_path"`
	MemoryStorePath    string   `json:"memory_store_path"`
	SupportedResources []string `json:"supported_resources"`
	SupportedTools     []string `json:"supported_tools"`
	Notes              []string `json:"notes"`
}

type runDetail struct {
	Run   *runstate.Record    `json:"run"`
	Proof *domain.ProofBundle `json:"proof,omitempty"`
}

type runLogs struct {
	RunID   string              `json:"run_id"`
	LogPath string              `json:"log_path,omitempty"`
	Entries []runstate.LogEntry `json:"entries"`
}

type refreshToolInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"optional maximum number of worktree directories to scan"`
}

type refreshToolOutput struct {
	Scanned int `json:"scanned"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
}

type startRunToolInput = operator.StartRunInput
type startRunToolOutput = operator.StartRunOutput

type cancelRunToolInput struct {
	RunID string `json:"run_id" jsonschema:"run identifier to cancel"`
}

type cancelRunToolOutput = operator.CancelRunOutput

type rememberToolInput struct {
	Key   string `json:"key" jsonschema:"unique identifier for the memory"`
	Value string `json:"value" jsonschema:"content to remember"`
	RunID string `json:"run_id,omitempty" jsonschema:"optional run identifier associated with this memory"`
}

type recallToolInput struct {
	Key string `json:"key" jsonschema:"identifier of the memory to recall"`
}

type forgetToolInput struct {
	Key string `json:"key" jsonschema:"identifier of the memory to forget"`
}

type runSkillToolInput struct {
	SkillName string `json:"skill_name" jsonschema:"name of the skill to execute"`
	Args      string `json:"args,omitempty" jsonschema:"optional arguments to pass to the skill command"`
}

type runSkillToolOutput struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
}

func New(cfg Config, opts Options) *Server {
	logOutput := opts.LogOutput
	if logOutput == nil {
		logOutput = os.Stdout
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	mcpPath := normalizePath(cfg.MCPPath)
	return &Server{
		cfg:         cfg,
		logger:      slog.New(slog.NewTextHandler(logOutput, nil)),
		now:         now,
		store:       runstate.New(cfg.RunStatePath),
		memoryStore: memory.New(cfg.MemoryStorePath),
		baseURL:     "http://" + cfg.ListenAddress + mcpPath,
		mcpPath:     mcpPath,
	}
}

func (s *Server) Handler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ariadne-mcp",
		Version: "0.1.0",
	}, &mcp.ServerOptions{
		Instructions: "Use Ariadne MCP resources for run inspection and the refresh_run_index tool to backfill or refresh run metadata from worktrees.",
		Logger:       s.logger,
	})

	server.AddResource(&mcp.Resource{
		Name:        "overview",
		Description: "Overview of the Ariadne MCP (legacy operator plane + gateway) and storage locations.",
		MIMEType:    "application/json",
		URI:         resourceOverview,
	}, s.readOverview)

	server.AddResource(&mcp.Resource{
		Name:        "runs",
		Description: "List of Ariadne runs from the shared run-state index.",
		MIMEType:    "application/json",
		URI:         resourceRuns,
	}, s.readRuns)

	server.AddResource(&mcp.Resource{
		Name:        "memory",
		Description: "Harness-wide persistent memory entries.",
		MIMEType:    "application/json",
		URI:         resourceMemory,
	}, s.readMemory)

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "run-detail",
		Description: "Inspect a single Ariadne run using ariadne://runs/{run_id}.",
		MIMEType:    "application/json",
		URITemplate: "ariadne://runs/{run_id}",
	}, s.readRunDetail)

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "run-logs",
		Description: "Read recent log lines for a run using ariadne://runs/{run_id}/logs.",
		MIMEType:    "application/json",
		URITemplate: "ariadne://runs/{run_id}/logs",
	}, s.readRunLogs)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "refresh_run_index",
		Description: "Scan Ariadne worktree directories and refresh the shared run index from run.jsonl and proof artifacts.",
	}, s.refreshRunIndex)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "start_run",
		Description: "Start a new manual Ariadne run from a title and description, using the configured routing or an explicitly pinned provider/persona.",
	}, s.startRun)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cancel_run",
		Description: "Request cancellation for a currently active Ariadne run started by this MCP server process.",
	}, s.cancelRun)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ariadne_remember",
		Description: "Store a piece of knowledge in the harness-wide persistent memory.",
	}, s.remember)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ariadne_recall",
		Description: "Retrieve a piece of knowledge from the harness-wide persistent memory by its key.",
	}, s.recall)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ariadne_forget",
		Description: "Delete a piece of knowledge from the harness-wide persistent memory.",
	}, s.forget)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ariadne_run_skill",
		Description: "Execute a pre-configured Ariadne skill command.",
	}, s.runSkill)

	baseHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Logger:       s.logger,
		JSONResponse: true,
	})

	mux := http.NewServeMux()
	mux.Handle(s.mcpPath, baseHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	return mux
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	server := &http.Server{
		Addr:              s.cfg.ListenAddress,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	err := server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve ariadne mcp server: %w", err)
	}
	<-done
	return nil
}

func (s *Server) readOverview(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	payload := overview{
		Service:         "ariadne-mcp",
		BaseURL:         s.baseURL,
		RepoRoot:        s.cfg.RepoRoot,
		WorktreeDir:     filepath.Join(s.cfg.RepoRoot, s.cfg.WorktreeDir),
		RunStatePath:    s.cfg.RunStatePath,
		MemoryStorePath: s.cfg.MemoryStorePath,
		SupportedResources: []string{
			resourceOverview,
			resourceRuns,
			resourceMemory,
			"ariadne://runs/{run_id}",
			"ariadne://runs/{run_id}/logs",
		},
		SupportedTools: []string{
			"refresh_run_index", "start_run", "cancel_run",
			"ariadne_remember", "ariadne_recall", "ariadne_forget", "ariadne_run_skill",
		},
		Notes: []string{
			"Run inspection is backed by the shared run-state index written by Ariadne.",
			"Memory tools provide harness-wide persistent knowledge storage.",
			"Skills allow executing pre-configured commands in the harness environment.",
		},
	}
	return jsonResource(req.Params.URI, payload)
}

func (s *Server) readRuns(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	runs, err := s.store.List()
	if err != nil {
		return nil, err
	}
	return jsonResource(req.Params.URI, runs)
}

func (s *Server) readMemory(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	entries, err := s.memoryStore.List()
	if err != nil {
		return nil, err
	}
	return jsonResource(req.Params.URI, entries)
}

func (s *Server) readRunDetail(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	runID, err := runIDFromURI(req.Params.URI, false)
	if err != nil {
		return nil, err
	}
	record, err := s.store.Get(runID)
	if err != nil {
		return nil, err
	}
	payload := runDetail{Run: record}
	if record.ProofPath != "" {
		if data, err := os.ReadFile(record.ProofPath); err == nil {
			var bundle domain.ProofBundle
			if json.Unmarshal(data, &bundle) == nil {
				payload.Proof = &bundle
			}
		}
	}
	return jsonResource(req.Params.URI, payload)
}

func (s *Server) readRunLogs(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	runID, err := runIDFromURI(req.Params.URI, true)
	if err != nil {
		return nil, err
	}
	record, err := s.store.Get(runID)
	if err != nil {
		return nil, err
	}
	entries := []runstate.LogEntry{}
	if record.LogPath != "" {
		entries, err = runstate.TailLog(record.LogPath, 50)
		if err != nil {
			return nil, err
		}
	}
	return jsonResource(req.Params.URI, runLogs{
		RunID:   runID,
		LogPath: record.LogPath,
		Entries: entries,
	})
}

func (s *Server) refreshRunIndex(ctx context.Context, req *mcp.CallToolRequest, input refreshToolInput) (*mcp.CallToolResult, refreshToolOutput, error) {
	scanned, updated, skipped, err := s.scanWorktrees(input.Limit)
	if err != nil {
		return nil, refreshToolOutput{}, err
	}
	output := refreshToolOutput{Scanned: scanned, Updated: updated, Skipped: skipped}
	return toolResult(fmt.Sprintf("scanned %d worktrees, updated %d records", scanned, updated), false), output, nil
}

func (s *Server) startRun(ctx context.Context, req *mcp.CallToolRequest, input startRunToolInput) (*mcp.CallToolResult, startRunToolOutput, error) {
	if s.cfg.Gateway != nil {
		inv := gateway.Invocation{
			Title:       input.Title,
			Prompt:      input.Description,
			Labels:      input.Labels,
			Provider:    input.Provider,
			Persona:     input.Persona,
			Routing:     input.Routing,
			PublishMode: input.PublishMode,
			SourceURL:   input.SourceURL,
			Source:      "mcp", // mark the adapter source
			// Timeout / Env / TaskID can be mapped if needed in future
		}
		if input.TaskID != "" {
			inv.ID = input.TaskID // use as hint
		}
		run, err := s.cfg.Gateway.Submit(ctx, inv)
		if err != nil {
			return nil, startRunToolOutput{}, err
		}
		// Map back to legacy output shape for tool compatibility during transition
		out := startRunToolOutput{
			RunID:       run.ID,
			TaskID:      run.TaskID,
			Provider:    run.Provider,
			Persona:     run.Persona,
			PublishMode: "", // not tracked in gateway Run yet
			Status:      run.Status,
			Worktree:    run.Worktree,
			StartedAt:   run.StartedAt,
		}
		return toolResult("manual run started (via gateway)", false), out, nil
	}

	// Legacy path (operator.Service). This is the old manual control plane.
	// It is exercised only if a caller explicitly supplies Operator (current
	// mcp command never does). See internal/operator for deprecation docs.
	if s.cfg.Operator == nil {
		return nil, startRunToolOutput{}, fmt.Errorf("neither gateway nor operator service is configured")
	}
	output, err := s.cfg.Operator.StartRun(ctx, operator.StartRunInput(input))
	if err != nil {
		return nil, startRunToolOutput{}, err
	}
	return toolResult("manual run started", false), startRunToolOutput(*output), nil
}

func (s *Server) cancelRun(ctx context.Context, req *mcp.CallToolRequest, input cancelRunToolInput) (*mcp.CallToolResult, cancelRunToolOutput, error) {
	if s.cfg.Gateway != nil {
		cancelled, err := s.cfg.Gateway.Cancel(strings.TrimSpace(input.RunID))
		if err != nil {
			return nil, cancelRunToolOutput{}, err
		}
		out := cancelRunToolOutput{
			RunID:           input.RunID,
			CancelRequested: cancelled,
		}
		return toolResult("cancel request recorded (via gateway)", false), out, nil
	}

	if s.cfg.Operator == nil {
		return nil, cancelRunToolOutput{}, fmt.Errorf("neither gateway nor operator service is configured")
	}
	// Legacy operator cancel path (see comment on Operator field).
	output, err := s.cfg.Operator.CancelRun(strings.TrimSpace(input.RunID))
	if err != nil {
		return nil, cancelRunToolOutput{}, err
	}
	return toolResult("cancel request recorded", false), cancelRunToolOutput(*output), nil
}

func (s *Server) remember(ctx context.Context, req *mcp.CallToolRequest, input rememberToolInput) (*mcp.CallToolResult, any, error) {
	if err := s.memoryStore.Remember(input.Key, input.Value, input.RunID); err != nil {
		return nil, nil, err
	}
	return toolResult(fmt.Sprintf("remembered %q", input.Key), false), nil, nil
}

func (s *Server) recall(ctx context.Context, req *mcp.CallToolRequest, input recallToolInput) (*mcp.CallToolResult, any, error) {
	val, ok, err := s.memoryStore.Recall(input.Key)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return toolResult(fmt.Sprintf("key %q not found in memory", input.Key), true), nil, nil
	}
	return toolResult(val, false), nil, nil
}

func (s *Server) forget(ctx context.Context, req *mcp.CallToolRequest, input forgetToolInput) (*mcp.CallToolResult, any, error) {
	if err := s.memoryStore.Forget(input.Key); err != nil {
		return nil, nil, err
	}
	return toolResult(fmt.Sprintf("forgot %q", input.Key), false), nil, nil
}

func (s *Server) runSkill(ctx context.Context, req *mcp.CallToolRequest, input runSkillToolInput) (*mcp.CallToolResult, runSkillToolOutput, error) {
	skill, ok := s.cfg.Skills[input.SkillName]
	if !ok {
		return nil, runSkillToolOutput{}, fmt.Errorf("skill %q not found", input.SkillName)
	}

	commandLine := skill.Command
	workingDir := s.cfg.RepoRoot

	if skill.IsPackage {
		// For SKILL.md packages, if no command is specified, try to find a main script.
		// We'll look for scripts/run.cjs, scripts/run.py, or scripts/run.sh
		if commandLine == "" {
			scriptsDir := filepath.Join(skill.Dir, "scripts")
			for _, entry := range []struct {
				file string
				cmd  string
			}{
				{"run.cjs", "node"},
				{"run.js", "node"},
				{"run.py", "python3"},
				{"run.sh", "bash"},
			} {
				scriptPath := filepath.Join(scriptsDir, entry.file)
				if _, err := os.Stat(scriptPath); err == nil {
					commandLine = entry.cmd + " " + scriptPath
					break
				}
			}
		}
		// If still no command, maybe it's an informational skill or has a custom command.
		if commandLine == "" {
			return nil, runSkillToolOutput{}, fmt.Errorf("skill %q has no executable command or run script", input.SkillName)
		}
		// Skills can run in their own directory or the repo root.
		// For Ariadne, we'll default to the repo root but allow the skill to be relative.
	}

	cmdParts := strings.Fields(commandLine)
	if len(cmdParts) == 0 {
		return nil, runSkillToolOutput{}, fmt.Errorf("skill %q has no command", input.SkillName)
	}

	if input.Args != "" {
		cmdParts = append(cmdParts, strings.Fields(input.Args)...)
	}

	command := exec.CommandContext(ctx, cmdParts[0], cmdParts[1:]...)
	command.Dir = workingDir
	command.Env = os.Environ()
	for k, v := range skill.Env {
		command.Env = append(command.Env, fmt.Sprintf("%s=%s", k, v))
	}

	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, runSkillToolOutput{}, fmt.Errorf("execute skill %q: %w", input.SkillName, err)
		}
	}

	output := runSkillToolOutput{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Exit:   exitCode,
	}
	return toolResult(fmt.Sprintf("skill %q executed with exit code %d", input.SkillName, exitCode), exitCode != 0), output, nil
}

func (s *Server) scanWorktrees(limit int) (int, int, int, error) {
	root := filepath.Join(s.cfg.RepoRoot, s.cfg.WorktreeDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, 0, nil
		}
		return 0, 0, 0, fmt.Errorf("read worktree dir: %w", err)
	}

	if limit > 0 && limit < len(entries) {
		entries = entries[:limit]
	}

	scanned := 0
	updated := 0
	skipped := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		scanned++
		record, ok, err := scanWorktree(filepath.Join(root, entry.Name()))
		if err != nil {
			return scanned, updated, skipped, err
		}
		if !ok {
			skipped++
			continue
		}
		if err := s.store.Upsert(record); err != nil {
			return scanned, updated, skipped, err
		}
		updated++
	}
	return scanned, updated, skipped, nil
}

func scanWorktree(dir string) (runstate.Record, bool, error) {
	record := runstate.Record{
		ID:           filepath.Base(dir),
		WorktreePath: dir,
	}

	info, err := os.Stat(dir)
	if err != nil {
		return runstate.Record{}, false, fmt.Errorf("stat worktree %s: %w", dir, err)
	}
	record.CreatedAt = info.ModTime().UTC()
	record.UpdatedAt = record.CreatedAt

	logPath := filepath.Join(dir, "run.jsonl")
	if logInfo, err := os.Stat(logPath); err == nil {
		record.LogPath = logPath
		if logInfo.ModTime().After(record.UpdatedAt) {
			record.UpdatedAt = logInfo.ModTime().UTC()
		}
		status, provider, taskID, lastEvent, lastError, startedAt, finishedAt, err := scanRunLog(logPath)
		if err != nil {
			return runstate.Record{}, false, err
		}
		record.Status = status
		record.Provider = provider
		record.TaskID = taskID
		record.LastEvent = lastEvent
		record.LastError = lastError
		if !startedAt.IsZero() {
			record.StartedAt = startedAt.UTC()
		}
		if finishedAt != nil {
			finishedUTC := finishedAt.UTC()
			record.FinishedAt = &finishedUTC
		}
	}

	proofPath := filepath.Join(dir, "proof", "summary.json")
	if proofInfo, err := os.Stat(proofPath); err == nil {
		record.ProofPath = proofPath
		if proofInfo.ModTime().After(record.UpdatedAt) {
			record.UpdatedAt = proofInfo.ModTime().UTC()
		}
		data, err := os.ReadFile(proofPath)
		if err != nil {
			return runstate.Record{}, false, fmt.Errorf("read proof summary %s: %w", proofPath, err)
		}
		var bundle domain.ProofBundle
		if err := json.Unmarshal(data, &bundle); err != nil {
			return runstate.Record{}, false, fmt.Errorf("decode proof summary %s: %w", proofPath, err)
		}
		record.TaskID = firstNonEmpty(record.TaskID, bundle.TaskID)
		record.Provider = firstNonEmpty(record.Provider, bundle.Provider)
		record.PRURL = bundle.PRUrl
		record.Walkthrough = bundle.Walkthrough
		record.DurationSeconds = bundle.DurationSeconds
		if record.Status == "" {
			record.Status = domain.RunStatusSucceeded
		}
		if record.LastEvent == "" {
			record.LastEvent = "proof_collected"
		}
	}

	if record.LogPath == "" && record.ProofPath == "" {
		return runstate.Record{}, false, nil
	}
	if record.Status == "" {
		record.Status = domain.RunStatusPending
	}
	return record, true, nil
}

func scanRunLog(path string) (domain.RunStatus, string, string, string, string, time.Time, *time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", "", "", time.Time{}, nil, fmt.Errorf("open run log %s: %w", path, err)
	}
	defer f.Close()

	var (
		status    domain.RunStatus
		provider  string
		taskID    string
		lastEvent string
		lastError string
		startedAt time.Time
		finished  *time.Time
	)

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		event, _ := raw["event"].(string)
		if event == "" {
			continue
		}
		lastEvent = event
		ts, _ := raw["timestamp"].(string)
		parsedTS, _ := time.Parse(time.RFC3339Nano, ts)
		switch event {
		case "run_started":
			status = domain.RunStatusRunning
			provider = stringOr(raw["provider"], provider)
			taskID = stringOr(raw["task_id"], taskID)
			if !parsedTS.IsZero() && startedAt.IsZero() {
				startedAt = parsedTS
			}
		case "run_succeeded":
			status = domain.RunStatusSucceeded
			if !parsedTS.IsZero() {
				t := parsedTS
				finished = &t
			}
		case "run_failed":
			status = domain.RunStatusFailed
			lastError = stringOr(raw["error"], lastError)
			if !parsedTS.IsZero() {
				t := parsedTS
				finished = &t
			}
		case "run_timeout":
			status = domain.RunStatusTimeout
			lastError = "run timed out"
			if !parsedTS.IsZero() {
				t := parsedTS
				finished = &t
			}
		case "provider_stdout":
			if status == "" {
				status = domain.RunStatusRunning
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", "", "", "", time.Time{}, nil, fmt.Errorf("scan run log %s: %w", path, err)
	}
	return status, provider, taskID, lastEvent, lastError, startedAt, finished, nil
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/mcp"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func runIDFromURI(uri string, logs bool) (string, error) {
	prefix := "ariadne://runs/"
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("invalid run URI %q", uri)
	}
	runID := strings.TrimPrefix(uri, prefix)
	if logs {
		if !strings.HasSuffix(runID, "/logs") {
			return "", fmt.Errorf("invalid run logs URI %q", uri)
		}
		runID = strings.TrimSuffix(runID, "/logs")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" || strings.Contains(runID, "/") {
		return "", fmt.Errorf("invalid run URI %q", uri)
	}
	return runID, nil
}

func jsonResource(uri string, payload any) (*mcp.ReadResourceResult, error) {
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal resource %q: %w", uri, err)
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      uri,
				MIMEType: "application/json",
				Text:     string(body),
			},
		},
	}, nil
}

func toolResult(message string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: isError,
		Content: []mcp.Content{
			&mcp.TextContent{Text: message},
		},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringOr(value any, fallback string) string {
	text, _ := value.(string)
	if strings.TrimSpace(text) == "" {
		return fallback
	}
	return text
}
