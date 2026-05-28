// Package operator implements the legacy control plane.
//
// This is the original "operator" Service from the pre-gateway era. It powers
// manual run starts (StartRun/CancelRun) and was historically associated with
// the polling-driven orchestrator (`ariadne run` for GitHub/Linear sources).
//
// # Legacy Status
//
// This package is retained **only for backward compatibility** during the
// gateway refactor (Phase 2+). It is not the recommended path for new code,
// direct invocations, or agent-driven runs.
//
//   - The modern recommended path is internal/gateway (Gateway + Executor).
//     See cmd/ariadne/mcp.go (post-Task A) which creates only a Gateway.
//   - The `ariadne run` polling loop (worksource poller + GitHub/Linear) still
//     uses duplicated manual wiring in cmd/ariadne/main.go:startOrchestrator.
//     That too is legacy; operator.Service is the "service" half of the old
//     manual control plane.
//   - The mcpserver retains an Operator *Service field purely as a fallback
//     during transition; current mcp.go never populates it.
//
// New callers should use gateway.BuildProvidersFromConfig,
// gateway.DefaultSupervisorForGateway, and gateway.NewSupervisorExecutor
// (with gateway.New) instead of constructing operator.Service directly.
//
// Where practical we delegate (provider construction now reuses the gateway
// helper to eliminate the copy of build logic). Full delegation of StartRun
// execution, routing, proof collection, and active-run tracking is left for
// later extraction to avoid behavior changes in the legacy paths.
//
// The package will eventually be removed once all call sites (mainly tests
// or any external direct usage of the Service) have migrated and the polling
// path itself is modernized.
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
	"github.com/haha-systems/ariadne/internal/gateway"
	"github.com/haha-systems/ariadne/internal/proof"
	"github.com/haha-systems/ariadne/internal/provider"
	"github.com/haha-systems/ariadne/internal/router"
	"github.com/haha-systems/ariadne/internal/runstate"
	"github.com/haha-systems/ariadne/internal/supervisor"
	"github.com/haha-systems/ariadne/internal/worksource"
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

// Service is the legacy orchestrator service.
//
// It encapsulates the old control plane components for manual/direct starts
// (StartRun + CancelRun with its own active-run tracking + manual worksource)
// plus the proof collection path that lives outside the supervisor for the
// polling/manual era.
//
// The router, proof collector, and manual source are legacy-specific and not
// yet factored into the gateway. Cancellation tracking uses a simple mutex map.
//
// Deprecated: Prefer creating a gateway.Gateway (with SupervisorExecutor) for
// all new usage, including MCP adapters. This type and its methods are kept
// solely for any remaining direct callers or the mcpserver legacy fallback
// (see internal/mcpserver/server.go). No new features will be added here.
type Service struct {
	cfg         *config.Config
	repoRoot    string
	router      *router.Router
	supervisor  *supervisor.Supervisor
	collector   *proof.Collector
	proofConfig proof.Config
	source      worksource.WorkSource
	stateStore  *runstate.Store
	globalEnv   map[string]string
	hooks       []string
	activeMu    sync.Mutex
	activeRuns  map[string]context.CancelFunc
}

// New constructs the legacy Service.
//
// It wires providers (now delegated), the legacy router (for StartRun routing),
// a supervisor, proof collector (with legacy RequirePRForIssue=true), and the
// manual worksource + per-run cancel tracking.
//
// Deprecated: Use gateway.New together with gateway.BuildProvidersFromConfig,
// gateway.DefaultSupervisorForGateway (or your own supervisor), and
// gateway.NewSupervisorExecutor instead. This constructor exists only for
// backward compatibility. Its observable behavior for legacy callers is
// unchanged.
//
// NOTE on partial delegation: we reuse gateway.BuildProvidersFromConfig to
// eliminate the duplicated provider adapter construction logic. We do not yet
// call DefaultSupervisorForGateway because this path manages its own runstate
// store (to inject ARIADNE_RUN_STATE_PATH into globalEnv for legacy tasks).
// The collector and manual source + active tracking are also legacy concerns.
func New(cfg *config.Config, repoRoot string) (*Service, error) {
	providers := gateway.BuildProvidersFromConfig(cfg)
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
		source:     worksource.NewManualSource(),
		stateStore: stateStore,
		globalEnv:  globalEnv,
		hooks:      append([]string(nil), cfg.Hooks...),
		activeRuns: make(map[string]context.CancelFunc),
	}, nil
}

func (s *Service) StartRun(ctx context.Context, input StartRunInput) (*StartRunOutput, error) {
	// Legacy manual run start path. Uses the old router (not gateway policy engine)
	// and the manual worksource. Proof collection happens post-execution here
	// (outside supervisor) for compatibility with the pre-gateway era.
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
	// Legacy cancel path using the in-memory activeRuns map populated by StartRun.
	// Gateway has its own equivalent cancellation tracking.
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
		Source:       s.source,
		ReviewSource: s.source,
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

	bundle, err := collector.Collect(ctx, run, task, taskEnv, p, s.source)
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

// NOTE: buildProviders was removed here (2026-05) in favor of delegating to
// gateway.BuildProvidersFromConfig. The old private implementation was an
// exact duplicate of the logic now canonically maintained in the gateway
// package (see supervisor_executor.go).

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

// WriteToolResult is a small helper historically used by the operator/manual
// run tooling to write JSON results. It is part of the legacy surface and is
// retained for compatibility. Prefer standard encoding/json in new code.
func WriteToolResult(path string, payload any) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
