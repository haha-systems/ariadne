package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	charmlog "github.com/charmbracelet/log"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/proof"
	"github.com/haha-systems/ariadne/internal/provider"
	"github.com/haha-systems/ariadne/internal/router"
	"github.com/haha-systems/ariadne/internal/runstate"
	"github.com/haha-systems/ariadne/internal/supervisor"
	"github.com/haha-systems/ariadne/internal/worksource"
)

var version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var cfgPath string

	root := &cobra.Command{
		Use:   "ariadne",
		Short: "Multi-provider coding agent orchestrator",
	}
	root.PersistentFlags().StringVar(&cfgPath, "config", "ariadne.toml", "path to ariadne.toml")

	root.AddCommand(
		runCmd(&cfgPath),
		collectProofCmd(&cfgPath),
		landCmd(&cfgPath),
		costCmd(&cfgPath),
		runsCmd(&cfgPath),
		inspectRunCmd(&cfgPath),
		monitorCmd(&cfgPath),
	)
	return root
}

// runCmd starts the polling loop.
func runCmd(cfgPath *string) *cobra.Command {
	var verbose bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start polling for tasks and running agents",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = godotenv.Load()

			charmlog.SetTimeFormat("15:04:05")
			charmlog.SetReportCaller(false)
			if verbose {
				charmlog.SetLevel(charmlog.DebugLevel)
			} else {
				charmlog.SetLevel(charmlog.InfoLevel)
			}

			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return startOrchestrator(ctx, cfg)
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable DEBUG-level logging")
	return cmd
}

// collectProofCmd runs proof collection for a completed run.
func collectProofCmd(cfgPath *string) *cobra.Command {
	var runID string

	cmd := &cobra.Command{
		Use:   "collect-proof",
		Short: "Collect proof artifacts for a completed run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = godotenv.Load()
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}

			worktreePath := filepath.Join(cfg.Sandbox.WorktreeDir, runID)
			summaryPath := filepath.Join(worktreePath, "proof", "summary.json")

			data, err := os.ReadFile(summaryPath)
			if err != nil {
				return fmt.Errorf("run %s: proof not found at %s: %w", runID, summaryPath, err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run-id", "", "run ID (required)")
	cmd.MarkFlagRequired("run-id") //nolint:errcheck
	return cmd
}

// landCmd safely lands a reviewed run.
func landCmd(cfgPath *string) *cobra.Command {
	var runID string

	cmd := &cobra.Command{
		Use:   "land",
		Short: "Rebase, re-run CI, and merge a reviewed run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = godotenv.Load()
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}

			worktreePath := filepath.Join(cfg.Sandbox.WorktreeDir, runID)
			lander := proof.NewLander(proof.Config{
				RequireCIPass: true,
				PRBaseBranch:  cfg.Proof.PRBaseBranch,
				Env:           cfg.Sandbox.Env,
			})

			sha, err := lander.Land(cmd.Context(), worktreePath)
			if err != nil {
				return fmt.Errorf("land failed: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Landed at %s\n", sha)
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run-id", "", "run ID to land (required)")
	cmd.MarkFlagRequired("run-id") //nolint:errcheck
	return cmd
}

// costCmd prints per-run cost information from proof/summary.json files.
func costCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "cost",
		Short: "Show cost summary for completed runs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = godotenv.Load()
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}

			entries, err := os.ReadDir(cfg.Sandbox.WorktreeDir)
			if err != nil {
				return fmt.Errorf("reading worktree dir %s: %w", cfg.Sandbox.WorktreeDir, err)
			}

			var total float64
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%-30s %-12s %-10s %s\n", "RUN ID", "PROVIDER", "COST (USD)", "TASK")
			fmt.Fprintf(w, "%s\n", "------------------------------------------------------------")

			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				summaryPath := filepath.Join(cfg.Sandbox.WorktreeDir, entry.Name(), "proof", "summary.json")
				data, err := os.ReadFile(summaryPath)
				if err != nil {
					continue // run may not have a proof yet
				}
				var bundle domain.ProofBundle
				if err := json.Unmarshal(data, &bundle); err != nil {
					continue
				}
				fmt.Fprintf(w, "%-30s %-12s $%-9.4f %s\n",
					bundle.RunID, bundle.Provider, bundle.CostUSD, bundle.TaskID)
				total += bundle.CostUSD
			}

			fmt.Fprintf(w, "%s\n", "------------------------------------------------------------")
			fmt.Fprintf(w, "%-30s %-12s $%.4f\n", "TOTAL", "", total)
			return nil
		},
	}
}

// startOrchestrator wires together all components and runs the main loop.
func startOrchestrator(ctx context.Context, cfg *config.Config) error {
	// Build enabled providers.
	providers := buildProviders(cfg)
	if len(providers) == 0 {
		return fmt.Errorf("no enabled providers configured")
	}

	providerNames := make([]string, 0, len(providers))
	for name := range providers {
		providerNames = append(providerNames, name)
	}

	charmlog.Info("ariadne starting",
		"providers", strings.Join(providerNames, ","),
		"interval", fmt.Sprintf("%ds", cfg.Ariadne.WorkIntervalSeconds),
		"max_concurrent", cfg.Ariadne.MaxConcurrentRuns,
	)

	// Build work source.
	source, err := buildWorkSource(cfg)
	if err != nil {
		return err
	}

	// Wire components.
	rt := router.NewWithPersonas(
		providers,
		cfg.Routing.LabelRoutes,
		cfg.Routing.PersonaRoutes,
		cfg.Personas,
		cfg.Routing.Strategy,
		cfg.Ariadne.DefaultProvider,
	)

	stateStore := runStateStore(cfg)
	globalEnv := mapsClone(cfg.Sandbox.Env)
	globalEnv[runStatePathEnv] = stateStore.Path()

	sup := supervisor.New(supervisor.Config{
		WorktreeBaseDir:   cfg.Sandbox.WorktreeDir,
		TimeoutMinutes:    cfg.Sandbox.TimeoutMinutes,
		PreserveOnFailure: cfg.Sandbox.PreserveOnFailure,
		RepoRoot:          repoRoot(),
		RunState:          stateStore,
		WorkflowFile:      cfg.Sandbox.WorkflowFile,
	})

	proofCollector := proof.New(proof.Config{
		RequireCIPass:     cfg.Proof.RequireCIPass,
		RequirePRForIssue: true,
		PRBaseBranch:      cfg.Proof.PRBaseBranch,
		Env:               cfg.Sandbox.Env,
	})

	poller := worksource.NewPoller(source, worksource.PollerConfig{
		IntervalSeconds:   cfg.Ariadne.WorkIntervalSeconds,
		MaxConcurrentRuns: cfg.Ariadne.MaxConcurrentRuns,
	})

	taskCh := poller.Run(ctx)

	for task := range taskCh {
		go func() {
			defer poller.Done()
			executeTask(ctx, task, rt, sup, proofCollector, source, cfg.Hooks, globalEnv)
		}()
	}

	charmlog.Info("ariadne stopped")
	return nil
}

func executeTask(
	ctx context.Context,
	task *domain.Task,
	rt *router.Router,
	sup *supervisor.Supervisor,
	collector *proof.Collector,
	source worksource.WorkSource,
	hooks []string,
	globalEnv map[string]string,
) {
	log := charmlog.With("task_id", task.ID)

	route, err := rt.Route(task)
	if err != nil {
		log.Error("routing failed", "error", err)
		return
	}

	if route.RaceN > 1 {
		providerNames := make([]string, len(route.Providers))
		for i, p := range route.Providers {
			providerNames[i] = p.Name()
		}
		log.Info("race started", "title", task.Title, "runners", route.RaceN, "providers", strings.Join(providerNames, ","))
		executeRace(ctx, task, route, sup, collector, source, hooks, log, globalEnv)
		return
	}

	p := route.Providers[0]
	personaName := ""
	if route.Persona != nil {
		personaName = route.Persona.Name
	}
	log.Info("task routed", "title", task.Title, "type", task.Type, "provider", p.Name(), "persona", personaName)
	executeRun(ctx, task, p, route.Persona, sup, collector, source, hooks, log, globalEnv)
}

// executeRace spawns N parallel runs and takes the first success.
func executeRace(
	ctx context.Context,
	task *domain.Task,
	route router.RouteResult,
	sup *supervisor.Supervisor,
	collector *proof.Collector,
	source worksource.WorkSource,
	hooks []string,
	log *charmlog.Logger,
	globalEnv map[string]string,
) {
	type outcome struct {
		result *supervisor.Result
		p      provider.AgentProvider
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan outcome, len(route.Providers))

	ts := tokenSource(source)
	for _, p := range route.Providers {
		go func() {
			run := &domain.Run{ID: newRunID(), TaskID: task.ID, Provider: p.Name()}
			result := sup.Execute(raceCtx, supervisor.RunRequest{
				Run:          run,
				Task:         task,
				Provider:     p,
				GlobalEnv:    globalEnv,
				Persona:      route.Persona,
				Source:       source,
				ReviewSource: source,
				TokenSource:  ts,
			})
			ch <- outcome{result: result, p: p}
		}()
	}

	var winner *supervisor.Result
	var winnerProvider provider.AgentProvider
	failures := 0

	for range len(route.Providers) {
		out := <-ch
		if out.result.Err == nil && winner == nil {
			winner = out.result
			winnerProvider = out.p
			cancel() // context cancellation stops the remaining runs
		} else {
			failures++
		}
	}

	if winner == nil {
		log.Error("all race runs failed", "count", len(route.Providers))
		source.PostResult(ctx, task, fmt.Sprintf("All %d race runs failed", len(route.Providers))) //nolint:errcheck
		return
	}

	log.Info("race winner", "provider", winnerProvider.Name(), "run_id", winner.Run.ID, "failures", failures)
	if err := finishRun(ctx, winner.Run, task, winnerProvider, collector, source, hooks, log, globalEnv); err != nil {
		handleRunFailure(ctx, task, err, source, log, winner.Run.ID)
	}
}

func executeRun(
	ctx context.Context,
	task *domain.Task,
	p provider.AgentProvider,
	persona *config.PersonaConfig,
	sup *supervisor.Supervisor,
	collector *proof.Collector,
	source worksource.WorkSource,
	hooks []string,
	log *charmlog.Logger,
	globalEnv map[string]string,
) {
	run := &domain.Run{ID: newRunID(), TaskID: task.ID, Provider: p.Name()}
	result := sup.Execute(ctx, supervisor.RunRequest{
		Run:          run,
		Task:         task,
		Provider:     p,
		GlobalEnv:    globalEnv,
		Persona:      persona,
		Source:       source,
		ReviewSource: source,
		TokenSource:  tokenSource(source),
	})
	if result.Err != nil {
		handleRunFailure(ctx, task, result.Err, source, log, run.ID)
		return
	}
	switch task.Type {
	case domain.TaskTypeReview, domain.TaskTypeRevise:
		return // outcome already recorded by supervisor
	case domain.TaskTypeRebase:
		return // outcome already recorded by supervisor via RecordRebaseOutcome
	}
	if err := finishRun(ctx, run, task, p, collector, source, hooks, log, globalEnv); err != nil {
		handleRunFailure(ctx, task, err, source, log, run.ID)
	}
}

func finishRun(
	ctx context.Context,
	run *domain.Run,
	task *domain.Task,
	p provider.AgentProvider,
	collector *proof.Collector,
	source worksource.WorkSource,
	hooks []string,
	log *charmlog.Logger,
	globalEnv map[string]string,
) error {
	log.Info("run succeeded", "run_id", run.ID)

	// Use per-task env if available.
	var taskEnv map[string]string
	if task.Config != nil {
		taskEnv = task.Config.Env
	}

	bundle, err := collector.Collect(ctx, run, task, taskEnv, p, source)
	if err != nil {
		if state := runStateStoreFromGlobal(globalEnv); state != nil {
			if updateErr := state.Update(run.ID, func(r *runstate.Record) error {
				r.Status = domain.RunStatusFailed
				r.LastEvent = "proof_failed"
				r.LastError = err.Error()
				return nil
			}); updateErr != nil {
				charmlog.Warn("runstate proof failure update failed", "run_id", run.ID, "error", updateErr)
			}
		}
		return fmt.Errorf("proof collection failed: %w", err)
	}
	if state := runStateStoreFromGlobal(globalEnv); state != nil {
		if err := state.Update(run.ID, func(r *runstate.Record) error {
			r.ProofPath = filepath.Join(run.WorktreePath, "proof", "summary.json")
			r.PRURL = bundle.PRUrl
			r.Walkthrough = bundle.Walkthrough
			r.LastEvent = "proof_collected"
			return nil
		}); err != nil {
			charmlog.Warn("runstate proof update failed", "run_id", run.ID, "error", err)
		}
	}

	if err := source.PostResult(ctx, task, formatProofSummary(bundle)); err != nil {
		log.Error("post result failed", "error", err)
		if state := runStateStoreFromGlobal(globalEnv); state != nil {
			if updateErr := state.Update(run.ID, func(r *runstate.Record) error {
				r.LastEvent = "post_result_failed"
				r.LastError = err.Error()
				return nil
			}); updateErr != nil {
				charmlog.Warn("runstate post result failure update failed", "run_id", run.ID, "error", updateErr)
			}
		}
	}

	// Run post-run hooks with the summary path.
	if len(hooks) > 0 {
		summaryPath := filepath.Join(run.WorktreePath, "proof", "summary.json")
		if err := proof.RunHooks(ctx, hooks, summaryPath); err != nil {
			log.Warn("post-run hook failed", "run_id", run.ID, "error", err)
		}
	}

	log.Info("run complete — worktree preserved", "run_id", run.ID, "path", run.WorktreePath)
	if state := runStateStoreFromGlobal(globalEnv); state != nil {
		if err := state.Update(run.ID, func(r *runstate.Record) error {
			r.LastEvent = "run_complete"
			return nil
		}); err != nil {
			charmlog.Warn("runstate completion update failed", "run_id", run.ID, "error", err)
		}
	}
	return nil
}

const runStatePathEnv = "ARIADNE_RUN_STATE_PATH"

func runStateStore(cfg *config.Config) *runstate.Store {
	return runstate.New(filepath.Join(repoRoot(), cfg.Sandbox.WorktreeDir, "index.json"))
}

func runStateStoreFromGlobal(env map[string]string) *runstate.Store {
	if env == nil {
		return nil
	}
	path := env[runStatePathEnv]
	if path == "" {
		return nil
	}
	return runstate.New(path)
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

type issueFailureRecorder interface {
	RecordIssueFailure(ctx context.Context, task *domain.Task, reason string) error
}

func handleRunFailure(ctx context.Context, task *domain.Task, err error, source worksource.WorkSource, log *charmlog.Logger, runID string) {
	log.Error("run failed", "run_id", runID, "error", err)
	switch task.Type {
	case domain.TaskTypeReview, domain.TaskTypeRevise:
		source.RecordReviewOutcome(ctx, task, false, err.Error()) //nolint:errcheck
	case domain.TaskTypeRebase:
		source.RecordRebaseOutcome(ctx, task, false, err.Error()) //nolint:errcheck
	default:
		if recorder, ok := source.(issueFailureRecorder); ok {
			if recordErr := recorder.RecordIssueFailure(ctx, task, err.Error()); recordErr != nil {
				log.Warn("record issue failure failed", "run_id", runID, "error", recordErr)
			}
		}
		source.PostResult(ctx, task, fmt.Sprintf("Run failed: %v", err)) //nolint:errcheck
	}
}

func formatProofSummary(b *domain.ProofBundle) string {
	data, _ := json.MarshalIndent(b, "", "  ")
	return "```json\n" + string(data) + "\n```"
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
			// Treat unknown names with a binary set as custom adapters.
			if pcfg.Binary != "" {
				providers[name] = provider.NewCustomAdapter(name, pcfg.Binary, pcfg.ExtraArgs, pcfg.CostPer1kTokens)
			} else {
				charmlog.Warn("unknown provider type with no binary, skipping", "name", name)
			}
		}
	}
	return providers
}

func buildWorkSource(cfg *config.Config) (worksource.WorkSource, error) {
	if cfg.WorkSources.GitHub != nil {
		token := os.Getenv("GITHUB_TOKEN")
		appID, keyPath := os.Getenv("GH_APP_APP_ID"), os.Getenv("GH_APP_PRIVATE_KEY_PATH")
		if token == "" && (appID == "" || keyPath == "") {
			return nil, fmt.Errorf("GitHub auth required: set GITHUB_TOKEN, or both GH_APP_APP_ID and GH_APP_PRIVATE_KEY_PATH")
		}
		return worksource.NewGitHubSource(token, cfg.WorkSources.GitHub.Repo, cfg.WorkSources.GitHub.LabelFilter, cfg.WorkSources.GitHub.AllowedAuthors)
	}
	if cfg.WorkSources.Linear != nil {
		token := os.Getenv("LINEAR_API_KEY")
		if token == "" {
			return nil, fmt.Errorf("LINEAR_API_KEY env var required for Linear work source")
		}
		return worksource.NewLinearSource(token, cfg.WorkSources.Linear.TeamID, cfg.WorkSources.Linear.Project, cfg.WorkSources.Linear.StateFilter)
	}
	return nil, fmt.Errorf("no work source configured — add [work_sources.github] or [work_sources.linear] to ariadne.toml")
}

// tokenSource extracts a supervisor.TokenSource from a WorkSource if the
// underlying implementation supports it. Returns nil for non-GitHub sources.
func tokenSource(ws worksource.WorkSource) supervisor.TokenSource {
	if ts, ok := ws.(supervisor.TokenSource); ok {
		return ts
	}
	return nil
}

func repoRoot() string {
	// Walk up from cwd to find the git root.
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir // fallback
		}
		dir = parent
	}
}

func newRunID() string {
	return fmt.Sprintf("run_%d", time.Now().UnixNano())
}
