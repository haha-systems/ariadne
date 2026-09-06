package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/invocation"
	"github.com/haha-systems/ariadne/internal/memory"
	"github.com/haha-systems/ariadne/internal/memory/ghostdive"
	"github.com/haha-systems/ariadne/internal/memory/inmemory"
	"github.com/haha-systems/ariadne/internal/provider"
	"github.com/haha-systems/ariadne/internal/research"
	"github.com/haha-systems/ariadne/internal/scheduler"
	"github.com/haha-systems/ariadne/internal/workspace"
)

func researchCmd(cfgPath *string) *cobra.Command {
	root := &cobra.Command{
		Use:   "research",
		Short: "Run and inspect research workflow projects",
	}
	root.AddCommand(researchRunCmd(cfgPath), researchStatusCmd(cfgPath), researchFrontierCmd(cfgPath))
	return root
}

func researchRunCmd(cfgPath *string) *cobra.Command {
	var maxCycles int
	cmd := &cobra.Command{
		Use:   "run <project>",
		Short: "Run the Ari -> QAC -> Astra research cycle for a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := args[0]
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			loop, cleanup, err := buildResearchLoop(cfg, project)
			if err != nil {
				return err
			}
			defer cleanup()
			return loop.Run(cmd.Context(), project, maxCycles)
		},
	}
	cmd.Flags().IntVar(&maxCycles, "max-cycles", 20, "maximum number of Ari cycles to run")
	return cmd
}

func researchStatusCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status <project>",
		Short: "Show a project's current frontier and recent events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := args[0]
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			store, err := buildMemoryStore(cfg)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if frontier, err := store.Frontier(cmd.Context(), project); err == nil {
				fmt.Fprintf(w, "Frontier:\n%s\n\n", frontier.Summary)
			} else {
				fmt.Fprintf(w, "Frontier: none recorded yet\n\n")
			}
			data, err := os.ReadFile(researchEventsPath(project))
			if err != nil {
				fmt.Fprintf(w, "No events recorded yet at %s\n", researchEventsPath(project))
				return nil
			}
			fmt.Fprint(w, string(data))
			return nil
		},
	}
}

func researchFrontierCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "frontier <project>",
		Short: "Print a project's current research frontier",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := args[0]
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			store, err := buildMemoryStore(cfg)
			if err != nil {
				return err
			}
			frontier, err := store.Frontier(cmd.Context(), project)
			if err != nil {
				return fmt.Errorf("no frontier recorded for project %q", project)
			}
			fmt.Fprintln(cmd.OutOrStdout(), frontier.Summary)
			return nil
		},
	}
}

func agentsCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List configured agent profiles",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			names := make([]string, 0, len(cfg.AgentProfiles))
			for name := range cfg.AgentProfiles {
				names = append(names, name)
			}
			sort.Strings(names)

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%-16s %-10s %-24s %-10s %-10s\n", "NAME", "PROVIDER", "MODEL", "COST", "SCARCITY")
			for _, name := range names {
				p := cfg.AgentProfiles[name]
				fmt.Fprintf(w, "%-16s %-10s %-24s %-10s %-10s\n", name, p.Provider, p.Model, p.CostClass, p.ScarcityClass)
			}
			return nil
		},
	}
}

func budgetCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "budget",
		Short: "Show configured QAC escalation budget limits",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			approval := cfg.QAC.Approval
			if approval == "" {
				approval = string(scheduler.ControlAutomatic)
			}
			fmt.Fprintf(w, "approval:              %s\n", approval)
			fmt.Fprintf(w, "max_calls_per_cycle:   %d\n", cfg.QAC.MaxCallsPerCycle)
			fmt.Fprintf(w, "max_calls_per_session: %d\n", cfg.QAC.MaxCallsPerSession)
			fmt.Fprintf(w, "cooldown_minutes:      %d\n", cfg.QAC.CooldownMinutes)
			fmt.Fprintln(w, "\n(live usage counts are only tracked for the duration of a single `ariadne research run` process)")
			return nil
		},
	}
}

func researchEventsPath(project string) string {
	return filepath.Join(".ariadne", "research", project, "run.jsonl")
}

// buildMemoryStore selects the memory.Store backend: Ghostdive when
// configured, otherwise the in-memory backend with no external dependency.
func buildMemoryStore(cfg *config.Config) (memory.Store, error) {
	if cfg.Memory.Ghostdive == nil {
		return inmemory.New(), nil
	}
	apiKey := os.Getenv("GHOSTDIVE_API_KEY")
	client := ghostdive.NewClient(cfg.Memory.Ghostdive.BaseURL, apiKey)
	return ghostdive.NewAdapter(client, cfg.Memory.Ghostdive.SpaceIDByProject, filepath.Join(".ariadne", "research")), nil
}

// buildProviderForProfile constructs the provider.AgentProvider an
// agentprofile.Profile names, reusing the same provider-type switch as the
// issue workflow's buildProviders (kept separate rather than exported,
// since the two call sites have slightly different lifecycles: one enabled
// provider per config entry vs. one provider per named agent profile).
func buildProviderForProfile(cfg *config.Config, profileName string) (provider.AgentProvider, error) {
	profile, ok := cfg.AgentProfiles[profileName]
	if !ok {
		return nil, fmt.Errorf("agent profile %q not found in [agent_profiles]", profileName)
	}
	pcfg, ok := cfg.Providers[profile.Provider]
	if !ok {
		return nil, fmt.Errorf("agent profile %q: provider %q not found in [providers]", profileName, profile.Provider)
	}
	switch profile.Provider {
	case "claude":
		return provider.NewClaudeCodeAdapter(pcfg.Binary, pcfg.ExtraArgs, pcfg.CostPer1kTokens), nil
	case "codex":
		return provider.NewCodexAdapter(pcfg.Binary, pcfg.ExtraArgs), nil
	case "gemini":
		return provider.NewGeminiCLIAdapter(pcfg.Binary, pcfg.ExtraArgs, pcfg.CostPer1kTokens), nil
	case "opencode":
		return provider.NewOpenCodeAdapter(pcfg.Binary, pcfg.ExtraArgs, pcfg.CostPer1kTokens), nil
	default:
		if pcfg.Binary != "" {
			return provider.NewCustomAdapter(profile.Provider, pcfg.Binary, pcfg.ExtraArgs, pcfg.CostPer1kTokens), nil
		}
		return nil, fmt.Errorf("agent profile %q: unknown provider type %q with no binary", profileName, profile.Provider)
	}
}

// buildResearchLoop wires a research.AriLoop from configuration: agent
// profiles resolve to providers, invocations run in a single shared
// ephemeral workspace (research invocations don't require a git worktree —
// see internal/workspace), QAC resolves to a scheduler.Scheduler, and
// memory resolves to Ghostdive or the in-memory backend. The returned
// cleanup func releases the ephemeral workspace and closes the event log.
func buildResearchLoop(cfg *config.Config, project string) (*research.AriLoop, func(), error) {
	projCfg, ok := cfg.Research.Projects[project]
	if !ok {
		return nil, nil, fmt.Errorf("no research project %q configured — add [research.projects.%s] to ariadne.toml", project, project)
	}

	ariProvider, err := buildProviderForProfile(cfg, projCfg.RoutineAgent)
	if err != nil {
		return nil, nil, fmt.Errorf("routine_agent: %w", err)
	}
	astraProvider, err := buildProviderForProfile(cfg, projCfg.FrontierAgent)
	if err != nil {
		return nil, nil, fmt.Errorf("frontier_agent: %w", err)
	}

	ws := workspace.EphemeralProvider{}
	wsHandle, err := ws.Acquire(context.Background(), workspace.KindEphemeral)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire workspace: %w", err)
	}

	store, err := buildMemoryStore(cfg)
	if err != nil {
		return nil, nil, err
	}

	mode := scheduler.ControlMode(cfg.QAC.Approval)
	if mode == "" {
		mode = scheduler.ControlAutomatic
	}
	budget := scheduler.NewBudget(cfg.QAC.MaxCallsPerCycle, cfg.QAC.MaxCallsPerSession, time.Duration(cfg.QAC.CooldownMinutes)*time.Minute)
	sched := scheduler.New(mode, budget, nil, projCfg.FrontierAgent)

	eventsPath := researchEventsPath(project)
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0755); err != nil {
		return nil, nil, fmt.Errorf("create research events dir: %w", err)
	}
	eventsFile, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, nil, fmt.Errorf("open research event log: %w", err)
	}
	events := invocation.NewEventWriter(eventsFile, "research-"+project)

	loop := &research.AriLoop{
		Ari:              invocation.ProviderRunner{Provider: ariProvider, RepoPath: wsHandle.Path},
		Astra:            invocation.ProviderRunner{Provider: astraProvider, RepoPath: wsHandle.Path},
		Decider:          sched,
		Memory:           store,
		Events:           events,
		AriProfileName:   projCfg.RoutineAgent,
		AstraProfileName: projCfg.FrontierAgent,
	}

	cleanup := func() {
		eventsFile.Close()
		ws.Release(context.Background(), wsHandle) //nolint:errcheck
	}
	return loop, cleanup, nil
}
