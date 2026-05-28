package mcpserver

import (
	"context"
	"encoding/json"
	"os/exec"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/operator"
	"github.com/haha-systems/ariadne/internal/runstate"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerExposesRunResourcesAndRefreshTool(t *testing.T) {
	repoRoot := t.TempDir()
	worktreeDir := ".ariadne/runs"
	runDir := filepath.Join(repoRoot, worktreeDir, "run_123")
	if err := os.MkdirAll(filepath.Join(runDir, "proof"), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	logBody := strings.Join([]string{
		`{"timestamp":"2026-04-09T20:00:00Z","event":"run_started","run_id":"run_123","provider":"codex","task_id":"HAHA-1"}`,
		`{"timestamp":"2026-04-09T20:01:00Z","event":"provider_stdout","line":"working"}`,
		`{"timestamp":"2026-04-09T20:02:00Z","event":"run_succeeded","run_id":"run_123"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(runDir, "run.jsonl"), []byte(logBody), 0o644); err != nil {
		t.Fatalf("write run log: %v", err)
	}
	proofBody := `{"run_id":"run_123","task_id":"HAHA-1","provider":"codex","pr_url":"http://example/pr/1","walkthrough":"done"}`
	if err := os.WriteFile(filepath.Join(runDir, "proof", "summary.json"), []byte(proofBody), 0o644); err != nil {
		t.Fatalf("write proof summary: %v", err)
	}

	cfg := Config{
		RepoRoot:        repoRoot,
		WorktreeDir:     worktreeDir,
		RunStatePath:    filepath.Join(repoRoot, worktreeDir, "index.json"),
		MemoryStorePath: filepath.Join(repoRoot, ".ariadne", "memory.json"),
		ListenAddress:   "127.0.0.1:7619",
		MCPPath:         "/mcp",
	}
	server := New(cfg, Options{Now: func() time.Time { return time.Date(2026, 4, 9, 20, 3, 0, 0, time.UTC) }})

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp",
	}, nil)
	if err != nil {
		t.Fatalf("connect mcp client: %v", err)
	}
	defer session.Close()

	overview, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: resourceOverview})
	if err != nil {
		t.Fatalf("read overview: %v", err)
	}
	if !strings.Contains(firstResourceText(t, overview), `"service": "ariadne-mcp"`) {
		t.Fatalf("unexpected overview: %s", firstResourceText(t, overview))
	}

	toolResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "refresh_run_index",
		Arguments: map[string]any{"limit": 10},
	})
	if err != nil {
		t.Fatalf("call refresh tool: %v", err)
	}
	if toolResult.IsError {
		t.Fatalf("unexpected tool error: %#v", toolResult)
	}

	runsResource, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: resourceRuns})
	if err != nil {
		t.Fatalf("read runs resource: %v", err)
	}
	var runs []runstate.Record
	if err := json.Unmarshal([]byte(firstResourceText(t, runsResource)), &runs); err != nil {
		t.Fatalf("decode runs resource: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run_123" {
		t.Fatalf("unexpected runs payload: %#v", runs)
	}

	detail, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "ariadne://runs/run_123"})
	if err != nil {
		t.Fatalf("read run detail: %v", err)
	}
	if !strings.Contains(firstResourceText(t, detail), `"pr_url": "http://example/pr/1"`) {
		t.Fatalf("unexpected run detail: %s", firstResourceText(t, detail))
	}

	logs, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "ariadne://runs/run_123/logs"})
	if err != nil {
		t.Fatalf("read run logs: %v", err)
	}
	if !strings.Contains(firstResourceText(t, logs), `"event": "provider_stdout"`) {
		t.Fatalf("unexpected run logs payload: %s", firstResourceText(t, logs))
	}
}

func TestServerStartAndCancelRunTools(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	cfg := &config.Config{
		Ariadne: config.AriadneConfig{
			MaxConcurrentRuns:   1,
			DefaultProvider:     "sleeper",
			WorkIntervalSeconds: 30,
		},
		Routing: config.RoutingConfig{
			Strategy:      "round-robin",
			LabelRoutes:   map[string]string{},
			PersonaRoutes: map[string]string{},
		},
		Proof: config.ProofConfig{
			PRBaseBranch: "main",
		},
		Sandbox: config.SandboxConfig{
			WorktreeDir:       ".ariadne/runs",
			TimeoutMinutes:    5,
			PreserveOnFailure: true,
			WorkflowFile:      ".ariadne/WORKFLOW.md",
			Env:               map[string]string{},
		},
		Providers: map[string]config.ProviderConfig{
			"sleeper": {
				Enabled:   true,
				Binary:    "/bin/sh",
				ExtraArgs: []string{"-c", "trap 'exit 0' TERM INT; while true; do sleep 1; done"},
			},
		},
		Personas: map[string]config.PersonaConfig{},
	}

	operatorSvc, err := operator.New(cfg, repoRoot)
	if err != nil {
		t.Fatalf("create operator service: %v", err)
	}

	server := New(Config{
		RepoRoot:        repoRoot,
		WorktreeDir:     cfg.Sandbox.WorktreeDir,
		RunStatePath:    filepath.Join(repoRoot, cfg.Sandbox.WorktreeDir, "index.json"),
		MemoryStorePath: filepath.Join(repoRoot, ".ariadne", "memory.json"),
		ListenAddress:   "127.0.0.1:7619",
		MCPPath:         "/mcp",
		Operator:        operatorSvc,
	}, Options{})

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp",
	}, nil)
	if err != nil {
		t.Fatalf("connect mcp client: %v", err)
	}
	defer session.Close()

	startResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "start_run",
		Arguments: map[string]any{
			"title":       "Manual test run",
			"description": "Keep running until cancelled",
		},
	})
	if err != nil {
		t.Fatalf("call start_run: %v", err)
	}
	if startResult.IsError {
		t.Fatalf("unexpected start_run tool error: %#v", startResult)
	}

	var started struct {
		RunID string `json:"run_id"`
	}
	if err := decodeStructured(startResult.StructuredContent, &started); err != nil {
		t.Fatalf("decode start_run output: %v", err)
	}
	if strings.TrimSpace(started.RunID) == "" {
		t.Fatal("start_run did not return a run ID")
	}

	cancelResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "cancel_run",
		Arguments: map[string]any{
			"run_id": started.RunID,
		},
	})
	if err != nil {
		t.Fatalf("call cancel_run: %v", err)
	}
	if cancelResult.IsError {
		t.Fatalf("unexpected cancel_run tool error: %#v", cancelResult)
	}

	var cancelled struct {
		RunID           string `json:"run_id"`
		CancelRequested bool   `json:"cancel_requested"`
	}
	if err := decodeStructured(cancelResult.StructuredContent, &cancelled); err != nil {
		t.Fatalf("decode cancel_run output: %v", err)
	}
	if cancelled.RunID != started.RunID || !cancelled.CancelRequested {
		t.Fatalf("unexpected cancel output: %#v", cancelled)
	}

	waitForRunRecord(t, filepath.Join(repoRoot, cfg.Sandbox.WorktreeDir, "index.json"), started.RunID)
}

func firstResourceText(t *testing.T, result *mcp.ReadResourceResult) string {
	t.Helper()
	if len(result.Contents) == 0 {
		t.Fatal("expected resource contents")
	}
	return result.Contents[0].Text
}

func decodeStructured(value any, dest any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "Ariadne Test")
	runGit(t, dir, "config", "user.email", "ariadne@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "init")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}

func waitForRunRecord(t *testing.T, indexPath, runID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(indexPath)
		if err == nil && strings.Contains(string(data), runID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("run %s did not appear in %s", runID, indexPath)
}
