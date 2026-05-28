package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	charmlog "github.com/charmbracelet/log"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/proof"
	"github.com/haha-systems/ariadne/internal/provider"
	"github.com/haha-systems/ariadne/internal/router"
	"github.com/haha-systems/ariadne/internal/runstate"
	"github.com/haha-systems/ariadne/internal/supervisor"
)

const runStatePathEnv = "ARIADNE_RUN_STATE_PATH"

type StartRunInput struct {
	TaskID         string            `json:"task_id,omitempty"`
	Title          string            `json:"title"`
	Description    string            `json:"description"`
	Labels         []string          `json:"labels,omitempty"`
	Provider       string            `json:"provider,omitempty"`
	Persona        string            `json:"persona,omitempty"`
	Routing        string            `json:"routing,omitempty"`
	PublishMode    string            `json:"publish_mode,omitempty"`
	SourceURL      string            `json:"source_url,omitempty"`
	TimeoutMinutes int               `json:"timeout_minutes,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
}

type StartRunOutput struct {
	RunID       string             `json:"run_id"`
	TaskID      string             `json:"task_id"`
	Provider    string             `json:"provider"`
	Persona     string             `json:"persona,omitempty"`
	PublishMode string             `json:"publish_mode"`
	Status      domain.RunStatus   `json:"status"`
	Worktree    string             `json:"worktree_path"`
	StartedAt   time.Time          `json:"started_at"`
	TaskConfig *domain.TaskConfig `json:"task_config,omitempty"`
}

type CancelRunOutput struct {
	RunID            string `json:"run_id"`
	CancelRequested  bool   `json:"cancel_requested"`
	AlreadyCompleted bool   `json:"already_completed,omitempty"`
}

type Service struct {
	cfg         *config.Config
	repoRoot    string
	router      *router.Router
	supervisor  *supervisor.Supervisor
	collector   *proof.Collector
	proofConfig proof.Config
	stateStore  *runstate.Store
	globalEnv   map[string]string
	hooks       []string
	activeMu    sync.Mutex
	activeRuns  map[string]context.CancelFunc
}

func New(cfg *config.Config, repoRoot string) (*Service, error) {
	providers := buildProviders(cfg)
	if len(providers) == 0 {
		return nil, fmt.Errorf("no enabled providers configured")
	}

	stateStore := runstate.New(filepath.Join(repoRoot, cfg.Sandbox.WorktreeDir, "index.json"))
	globalEnv := mapsClone(cfg.Sandbox.Env)
	globalEnv[runStatePathEnv] = stateStore.Path()

	return &Service{
		cfg:      cfg,
		repoRoot: repoRoot,
		router: router.NewWithPersonas(
			providers,
			cfg.Routing.LabelRoutes,
			cfg.Routing.PersonaRoutes,
			cfg.Personas,
			cfg.Routing.Strategy,
			cfg.Ariadne.DefaultProvider,
			cfg.Routing.RouterFile,
		),
		supervisor: supervisor.New(supervisor.Config{
			WorktreeBaseDir:   cfg.Sandbox.WorktreeDir,
			TimeoutMinutes:    cfg.Sandbox.TimeoutMinutes,
			PreserveOnFailure: cfg.Sandbox.PreserveOnFailure,
			RepoRoot:          repoRoot,
			RunState:          stateStore,
			WorkflowFile:      cfg.Sandbox.WorkflowFile,
		}),
		collector: proof.New(proof.Config{
			RequireCIPass:     cfg.Proof.RequireCIPass,
			RequirePRForIssue: true,
			PRBaseBranch:      cfg.Proof.PRBaseBranch,
			PublishMode:       cfg.Proof.PublishMode,
			CICommand:         cfg.Proof.CICommand,
			Env:               cfg.Sandbox.Env,
		}),
		proofConfig: proof.Config{
			RequireCIPass:     cfg.Proof.RequireCIPass,
			RequirePRForIssue: true,
			PRBaseBranch:      cfg.Proof.PRBaseBranch,
			PublishMode:       cfg.Proof.PublishMode,
			CICommand:         cfg.Proof.CICommand,
			Env:               cfg.Sandbox.Env,
		},
		// NOTE: The `source` field (formerly initialized with worksource.NewManualSource())
		// was removed as part of the gateway transition in Phase 2. ManualSource was a
		// no-op WorkSource only needed for the legacy operator-based direct-run path.
		// The modern gateway + SupervisorExecutor handles direct runs without any
		// WorkSource. The operator package remains legacy; nil is safe to pass where
		// Source/ReviewSource/notifier were previously provided (see executeManualRun).
		stateStore: stateStore,
		globalEnv:  globalEnv,
		hooks:      append([]string(nil), cfg.Hooks...),
		activeRuns: make(map[string]context.CancelFunc),
	}, nil
}

func (s *Service) StartRun(ctx context.Context, input StartRunInput) (*StartRunOutput, error) {
	task, err := s.buildTask(input)
	if err != nil {
		return nil, err
	}

	route, err := s.router.Route(task)
	if err != nil {
		return nil, err
	}
	if route.RaceN > 1 {
		return nil, fmt.Errorf("manual start does not support race routing; pin a provider explicitly")
	}

	run := &domain.Run{
		ID:       newRunID(),
		TaskID:   task.ID,
		Provider: route.Providers[0].Name(),
	}
	startedAt := time.Now().UTC()
	worktreePath := filepath.Join(s.repoRoot, s.cfg.Sandbox.WorktreeDir, run.ID)

	personaName := ""
	if route.Persona != nil {
		personaName = route.Persona.Name
	}
	publishMode := normalizePublishMode(input.PublishMode, s.proofConfig.PublishMode)

	if err := s.stateStore.Upsert(runstate.Record{
		ID:           run.ID,
		TaskID:       task.ID,
		TaskTitle:    task.Title,
		TaskType:     task.Type,
		TaskSource:   task.Source,
		SourceURL:    task.SourceURL,
		Provider:     run.Provider,
		Persona:      personaName,
		Status:       domain.RunStatusRunning,
		WorktreePath: worktreePath,
		StartedAt:    startedAt,
		LastEvent:    "queued",
		Metadata: map[string]string{
			"publish_mode": publishMode,
		},
	}); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s.trackRun(run.ID, cancel)

	go s.executeManualRun(runCtx, run, task, route.Providers[0], route.Persona, publishMode)

	return &StartRunOutput{
		RunID:       run.ID,
		TaskID:      task.ID,
		Provider:    run.Provider,
		Persona:     personaName,
		PublishMode: publishMode,
		Status:      domain.RunStatusRunning,
		Worktree:    worktreePath,
		StartedAt:   startedAt,
		TaskConfig: task.Config,
	}, nil
}

func (s *Service) CancelRun(runID string) (*CancelRunOutput, error) {
	s.activeMu.Lock()
	cancel, ok := s.activeRuns[runID]
	s.activeMu.Unlock()
	if ok {
		if err := s.stateStore.Update(runID, func(r *runstate.Record) error {
			r.LastEvent = "cancel_requested"
			return nil
		}); err != nil {
			charmlog.Warn("runstate cancel update failed", "run_id", runID, "error", err)
		}
		cancel()
		return &CancelRunOutput{RunID: runID, CancelRequested: true}, nil
	}

	record, err := s.stateStore.Get(runID)
	if err == nil {
		return &CancelRunOutput{
			RunID:            runID,
			CancelRequested:  false,
			AlreadyCompleted: record.Status != domain.RunStatusRunning,
		}, nil
	}
	return nil, fmt.Errorf("run %s is not known or not active", runID)
}

func (s *Service) executeManualRun(ctx context.Context, run *domain.Run, task *domain.Task, p provider.AgentProvider, persona *config.PersonaConfig, publishMode string) {
	defer s.untrackRun(run.ID)

	result := s.supervisor.Execute(ctx, supervisor.RunRequest{
		Run:          run,
		Task:         task,
		Provider:     p,
		GlobalEnv:    mapsClone(s.globalEnv),
		Persona:      persona,
		Source:       nil, // ManualSource removed (gateway transition); nil is safe for Issue tasks per supervisor.RunRequest docs
		ReviewSource: nil,
		TokenSource:  nil,
	})

	if result.Err != nil {
		return
	}
	if task.Type != domain.TaskTypeIssue {
		return
	}

	taskEnv := map[string]string{}
	if task.Config != nil && len(task.Config.Env) > 0 {
		taskEnv = task.Config.Env
	}

	collector := s.collector
	if publishMode != normalizePublishMode("", s.proofConfig.PublishMode) {
		cfg := s.proofConfig
		cfg.PublishMode = publishMode
		collector = proof.New(cfg)
	}

	bundle, err := collector.Collect(ctx, run, task, taskEnv, p, nil) // ManualSource removed (gateway transition); nil notifier is safe per collector.Collect docs
	if err != nil {
		_ = s.stateStore.Update(run.ID, func(r *runstate.Record) error {
			r.Status = domain.RunStatusFailed
			r.LastEvent = "proof_failed"
			r.LastError = err.Error()
			return nil
		})
		return
	}

	_ = s.stateStore.Update(run.ID, func(r *runstate.Record) error {
		r.ProofPath = filepath.Join(run.WorktreePath, "proof", "summary.json")
		r.PRURL = bundle.PRUrl
		r.Walkthrough = bundle.Walkthrough
		r.LastEvent = "proof_collected"
		return nil
	})

	if len(s.hooks) > 0 {
		summaryPath := filepath.Join(run.WorktreePath, "proof", "summary.json")
		if err := proof.RunHooks(ctx, s.hooks, summaryPath); err != nil {
			charmlog.Warn("post-run hook failed", "run_id", run.ID, "error", err)
		}
	}

	_ = s.stateStore.Update(run.ID, func(r *runstate.Record) error {
		r.LastEvent = "run_complete"
		return nil
	})
}

func (s *Service) buildTask(input StartRunInput) (*domain.Task, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	description := strings.TrimSpace(input.Description)
	if description == "" {
		return nil, fmt.Errorf("description is required")
	}

	parsedConfig, _ := domain.ParseFrontMatter(description)
	taskConfig := mergeTaskConfig(parsedConfig, input)
	taskID := strings.TrimSpace(input.TaskID)
	if taskID == "" {
		taskID = fmt.Sprintf("manual-%d", time.Now().UnixNano())
	}
	now := time.Now().UTC()

	return &domain.Task{
		ID:          taskID,
		Title:       title,
		Description: description,
		Labels:      append([]string(nil), input.Labels...),
		Config:      taskConfig,
		Status:      domain.TaskStatusClaimed,
		Source:      "manual",
		SourceURL:   strings.TrimSpace(input.SourceURL),
		CreatedAt:   now,
		UpdatedAt:   now,
		Type:        domain.TaskTypeIssue,
	}, nil
}

func mergeTaskConfig(base *domain.TaskConfig, input StartRunInput) *domain.TaskConfig {
	var cfg domain.TaskConfig
	if base != nil {
		cfg = *base
		cfg.Env = mapsClone(base.Env)
	} else {
		cfg.Env = map[string]string{}
	}
	if strings.TrimSpace(input.Provider) != "" {
		cfg.Agent = strings.TrimSpace(input.Provider)
	}
	if strings.TrimSpace(input.Persona) != "" {
		cfg.Persona = strings.TrimSpace(input.Persona)
	}
	if strings.TrimSpace(input.Routing) != "" {
		cfg.Routing = strings.TrimSpace(input.Routing)
	}
	if input.TimeoutMinutes > 0 {
		cfg.TimeoutMinutes = input.TimeoutMinutes
	}
	for k, v := range input.Env {
		cfg.Env[k] = v
	}
	if cfg.Agent == "" && cfg.Persona == "" && cfg.Routing == "" && cfg.TimeoutMinutes == 0 && len(cfg.Env) == 0 {
		return nil
	}
	return &cfg
}

func (s *Service) trackRun(runID string, cancel context.CancelFunc) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	s.activeRuns[runID] = cancel
}

func (s *Service) untrackRun(runID string) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	delete(s.activeRuns, runID)
}

func mapsClone(src map[string]string) map[string]string {
	if len(src) == 0 {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func buildProviders(cfg *config.Config) map[string]provider.AgentProvider {
	providers := make(map[string]provider.AgentProvider)
	for name, pcfg := range cfg.Providers {
		if !pcfg.Enabled {
			continue
		}
		switch name {
		case "claude":
			providers[name] = provider.NewClaudeCodeAdapter(pcfg.Binary, pcfg.ExtraArgs, pcfg.CostPer1kTokens)
		case "codex":
			providers[name] = provider.NewCodexAdapter(pcfg.Binary, pcfg.ExtraArgs)
		case "gemini":
			providers[name] = provider.NewGeminiCLIAdapter(pcfg.Binary, pcfg.ExtraArgs, pcfg.CostPer1kTokens)
		case "opencode":
			providers[name] = provider.NewOpenCodeAdapter(pcfg.Binary, pcfg.ExtraArgs, pcfg.CostPer1kTokens)
		default:
			if pcfg.Binary != "" {
				providers[name] = provider.NewCustomAdapter(name, pcfg.Binary, pcfg.ExtraArgs, pcfg.CostPer1kTokens)
			}
		}
	}
	return providers
}

func newRunID() string {
	return fmt.Sprintf("run_%d", time.Now().UnixNano())
}

func normalizePublishMode(override, fallback string) string {
	mode := strings.ToLower(strings.TrimSpace(override))
	switch mode {
	case "required", "allowed", "skip":
		return mode
	case "":
		switch strings.ToLower(strings.TrimSpace(fallback)) {
		case "allowed", "skip":
			return strings.ToLower(strings.TrimSpace(fallback))
		default:
			return "required"
		}
	default:
		return "required"
	}
}

func WriteToolResult(path string, payload any) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
