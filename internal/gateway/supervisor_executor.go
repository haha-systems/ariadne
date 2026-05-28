package gateway

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	charmlog "github.com/charmbracelet/log"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/provider"
	"github.com/haha-systems/ariadne/internal/runstate"
	"github.com/haha-systems/ariadne/internal/supervisor"
)

// SupervisorExecutor is a real Executor implementation that drives the
// existing high-quality supervisor for normal (non-rebase/review/revise) runs.
//
// This is the bridge for Phase 1/2. It lets the Gateway own the entry point
// while we still reuse the proven worktree + logging + timeout + persona logic
// inside supervisor.
//
// Over time, more of the supervisor's happy-path logic can migrate into a
// cleaner internal engine that this (or a future) Executor calls.
type SupervisorExecutor struct {
	repoRoot  string
	sup       *supervisor.Supervisor
	providers map[string]provider.AgentProvider
	personas  map[string]config.PersonaConfig
}

// NewSupervisorExecutor constructs an Executor backed by the given supervisor
// and provider/persona maps. The caller (usually the code that creates the
// Gateway) is responsible for building the providers and supervisor with the
// appropriate RunState, workflow file, etc.
func NewSupervisorExecutor(
	repoRoot string,
	sup *supervisor.Supervisor,
	providers map[string]provider.AgentProvider,
	personas map[string]config.PersonaConfig,
) *SupervisorExecutor {
	return &SupervisorExecutor{
		repoRoot:  repoRoot,
		sup:       sup,
		providers: providers,
		personas:  personas,
	}
}

// Execute implements Executor.
func (e *SupervisorExecutor) Execute(ctx context.Context, runID string, inv Invocation) (string, error) {
	// We need the provider name. For now we assume the gateway already resolved
	// it into the Invocation or we fall back. In a fuller implementation the
	// gateway would have resolved routing before calling Execute.
	providerName := inv.Provider
	if providerName == "" {
		providerName = "unknown"
	}

	p, ok := e.providers[providerName]
	if !ok {
		return "", fmt.Errorf("provider %q not found or not enabled", providerName)
	}

	var persona *config.PersonaConfig
	if inv.Persona != "" {
		if per, ok := e.personas[inv.Persona]; ok {
			persona = &per
		} else {
			charmlog.Warn("requested persona not found, proceeding without it", "persona", inv.Persona)
		}
	}

	now := time.Now().UTC()
	task := &domain.Task{
		ID:          runID,
		Title:       inv.Title,
		Description: inv.Prompt,
		Labels:      append([]string(nil), inv.Labels...),
		Status:      domain.TaskStatusClaimed,
		Source:      inv.Source,
		SourceURL:   "",
		CreatedAt:   now,
		UpdatedAt:   now,
		Type:        domain.TaskTypeIssue,
	}

	domRun := &domain.Run{
		ID:       runID,
		TaskID:   task.ID,
		Provider: providerName,
	}

	req := supervisor.RunRequest{
		Run:      domRun,
		Task:     task,
		Provider: p,
		GlobalEnv: map[string]string{},
		Persona: persona,
	}

	result := e.sup.Execute(ctx, req)

	worktree := domRun.WorktreePath
	if result.Err != nil {
		return worktree, result.Err
	}
	return worktree, nil
}

// BuildProvidersFromConfig is a small helper so callers don't have to copy the
// buildProviders logic from main.go / operator. We can move the canonical
// version later.
func BuildProvidersFromConfig(cfg *config.Config) map[string]provider.AgentProvider {
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

// DefaultSupervisorForGateway is a convenience that creates a Supervisor with
// reasonable defaults for direct/gateway-driven runs (RunState enabled, etc.).
// Callers can still create their own Supervisor and pass it to NewSupervisorExecutor.
func DefaultSupervisorForGateway(cfg *config.Config, repoRoot string) *supervisor.Supervisor {
	statePath := filepath.Join(repoRoot, cfg.Sandbox.WorktreeDir, "index.json")
	runState := runstate.New(statePath)

	return supervisor.New(supervisor.Config{
		WorktreeBaseDir:   cfg.Sandbox.WorktreeDir,
		TimeoutMinutes:    cfg.Sandbox.TimeoutMinutes,
		PreserveOnFailure: cfg.Sandbox.PreserveOnFailure,
		RepoRoot:          repoRoot,
		RunState:          runState,
		WorkflowFile:      cfg.Sandbox.WorkflowFile,
	})
}


