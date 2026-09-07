package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/control"
	"github.com/haha-systems/ariadne/internal/invocation"
	"github.com/haha-systems/ariadne/internal/research"
	"github.com/haha-systems/ariadne/internal/scheduler"
	"github.com/haha-systems/ariadne/internal/tui"
	"github.com/haha-systems/ariadne/internal/workspace"
)

// researchTuiCmd adds the interactive interface as one more way to drive a
// research project. It is deliberately additive: `research run`, `status`,
// `frontier`, `guidance`, `escalations`, `budget`, and `agents` all keep
// working unchanged, and nothing essential lives only in TUI code.
func researchTuiCmd(cfgPath *string) *cobra.Command {
	var maxCycles int
	cmd := &cobra.Command{
		Use:   "tui <project>",
		Short: "Watch and steer a research project interactively",
		Long: "Runs the Ari -> QAC -> Astra research cycle with a terminal interface for\n" +
			"observing agents, inspecting QAC decisions, steering the programme, and\n" +
			"pausing safely. The research runtime is the same one `research run` uses.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := args[0]
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			runtime, cleanup, err := buildResearchRuntime(ctx, cfg, project, maxCycles)
			if err != nil {
				return err
			}
			defer cleanup()

			runtime.Start(ctx)

			if err := tui.Run(ctx, runtime); err != nil {
				return err
			}

			// Leaving the interface does not end the run: give an
			// already-finishing loop a moment to settle, then report its
			// outcome rather than silently dropping it.
			select {
			case <-runtime.Done():
				return runtime.Err()
			case <-time.After(2 * time.Second):
				fmt.Fprintln(cmd.ErrOrStderr(), "research is still running; it will stop at its next safe boundary")
				return nil
			}
		},
	}
	cmd.Flags().IntVar(&maxCycles, "max-cycles", 20, "maximum number of Ari cycles to run")
	return cmd
}

// buildResearchRuntime wires the same research loop `research run` builds,
// plus the live event bus, per-agent log streams, and the control surface
// the interface needs.
//
// The wiring order matters under manual QAC: the scheduler needs the
// runtime as its Approver and the runtime needs the scheduler for budget
// state, so the loop is built first, the runtime second, and the scheduler
// attached last via SetDecider.
func buildResearchRuntime(ctx context.Context, cfg *config.Config, project string, maxCycles int) (*research.Runtime, func(), error) {
	projCfg, ok := cfg.Research.Projects[project]
	if !ok {
		return nil, nil, fmt.Errorf("no research project %q configured — add [research.projects.%s] to ariadne.toml", project, project)
	}
	if projCfg.Repo == "" {
		return nil, nil, fmt.Errorf("research project %q has no repo configured — set [research.projects.%s] repo = \"/path/to/repo\"", project, project)
	}

	ariProvider, err := buildProviderForProfile(cfg, projCfg.RoutineAgent)
	if err != nil {
		return nil, nil, fmt.Errorf("routine_agent: %w", err)
	}
	astraProvider, err := buildProviderForProfile(cfg, projCfg.FrontierAgent)
	if err != nil {
		return nil, nil, fmt.Errorf("frontier_agent: %w", err)
	}

	repoRoot, err := filepath.Abs(projCfg.Repo)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve repo path: %w", err)
	}
	ws := &workspace.PersistentGitProvider{RepoRoot: repoRoot, Path: filepath.Join(".ariadne", "research", project, "workspace")}
	wsHandle, err := ws.Acquire(ctx, workspace.KindWorktreeBacked)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire research workspace: %w", err)
	}

	// Memory is validated before anything expensive starts, exactly as the
	// non-interactive path does.
	store, strictPersistence, err := resolveResearchMemory(ctx, cfg, project, os.Stderr)
	if err != nil {
		ws.Release(context.Background(), wsHandle) //nolint:errcheck
		return nil, nil, err
	}

	eventsPath := researchEventsPath(project)
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0755); err != nil {
		ws.Release(context.Background(), wsHandle) //nolint:errcheck
		return nil, nil, fmt.Errorf("create research events dir: %w", err)
	}
	eventsFile, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		ws.Release(context.Background(), wsHandle) //nolint:errcheck
		return nil, nil, fmt.Errorf("open research event log: %w", err)
	}

	bus := control.NewBus()
	logs := control.NewLogBus(2000)

	// The JSONL writer comes first so an event is durably recorded before
	// any subscriber can observe it.
	events := invocation.MultiSink{
		invocation.NewEventWriter(eventsFile, "research-"+project),
		bus,
	}

	loop := &research.AriLoop{
		Ari: invocation.ProviderRunner{
			Provider: ariProvider, RepoPath: wsHandle.Path,
			LogWriter: logs.Writer(project, research.AgentAri, control.StreamStdout),
		},
		Astra: invocation.ProviderRunner{
			Provider: astraProvider, RepoPath: wsHandle.Path,
			LogWriter: logs.Writer(project, research.AgentAstra, control.StreamStdout),
		},
		Memory:            store,
		Events:            events,
		AriProfileName:    projCfg.RoutineAgent,
		AstraProfileName:  projCfg.FrontierAgent,
		AriModel:          cfg.AgentProfiles[projCfg.RoutineAgent].Model,
		AstraModel:        cfg.AgentProfiles[projCfg.FrontierAgent].Model,
		Guidance:          loadResearchGuidance(projCfg, project),
		StrictPersistence: strictPersistence,
	}

	mode := scheduler.ControlMode(cfg.QAC.Approval)
	if mode == "" {
		mode = scheduler.ControlAutomatic
	}

	runtime := research.NewRuntime(research.RuntimeConfig{
		Project:       project,
		Loop:          loop,
		Memory:        store,
		Bus:           bus,
		Logs:          logs,
		MaxCycles:     maxCycles,
		QACMode:       string(mode),
		MemoryBackend: memoryBackendName(cfg),
		EventReplay:   &research.JSONLReplayer{Path: eventsPath},
		Agents:        researchAgentSnapshots(cfg, projCfg),
	})

	// The runtime is the Approver: under manual mode the scheduler blocks
	// on it until a human answers.
	budget := scheduler.NewBudget(cfg.QAC.MaxCallsPerCycle, cfg.QAC.MaxCallsPerSession, time.Duration(cfg.QAC.CooldownMinutes)*time.Minute)
	runtime.SetDecider(scheduler.New(mode, budget, runtime, projCfg.FrontierAgent))

	cleanup := func() {
		bus.Close()
		logs.Close()
		eventsFile.Close()
		ws.Release(context.Background(), wsHandle) //nolint:errcheck
	}
	return runtime, cleanup, nil
}

// researchAgentSnapshots builds the static agent descriptors from
// configuration. Model and provider are labels for display; the loop and
// scheduler never reason about them.
func researchAgentSnapshots(cfg *config.Config, projCfg config.ResearchProjectConfig) []control.AgentSnapshot {
	describe := func(name, role string) control.AgentSnapshot {
		profile := cfg.AgentProfiles[name]
		return control.AgentSnapshot{
			Name:          agentRoleName(role),
			Model:         profile.Model,
			Provider:      profile.Provider,
			Role:          role,
			State:         control.AgentIdle,
			CostClass:     profile.CostClass,
			ScarcityClass: profile.ScarcityClass,
		}
	}
	return []control.AgentSnapshot{
		describe(projCfg.RoutineAgent, "routine"),
		describe(projCfg.FrontierAgent, "frontier"),
	}
}

// agentRoleName maps a workflow role onto the identity the loop reports, so
// the interface labels agents the same way events do.
func agentRoleName(role string) string {
	if role == "frontier" {
		return research.AgentAstra
	}
	return research.AgentAri
}

func memoryBackendName(cfg *config.Config) string {
	if cfg.Memory.Ghostdive != nil {
		return "ghostdive"
	}
	return "inmemory"
}
