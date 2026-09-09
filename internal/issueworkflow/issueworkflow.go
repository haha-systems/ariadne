// Package issueworkflow contains the existing GitHub/Linear issue-driven
// orchestration behavior, extracted verbatim from cmd/ariadne/main.go so it
// is testable and so the generic runtime (invocation, scheduler, memory,
// research workflow) doesn't need to assume every workflow originates from
// an issue, requires a PR, or has a review cycle. This is a mechanical
// extraction — behavior is unchanged from the pre-v2 orchestrator.
package issueworkflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	charmlog "github.com/charmbracelet/log"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/proof"
	"github.com/haha-systems/ariadne/internal/provider"
	"github.com/haha-systems/ariadne/internal/router"
	"github.com/haha-systems/ariadne/internal/session"
	"github.com/haha-systems/ariadne/internal/supervisor"
	"github.com/haha-systems/ariadne/internal/worksource"
)

// Workflow runs the polling loop: poll a WorkSource, route each task to a
// provider, execute it in an isolated worktree, and post the result back.
type Workflow struct {
	router     *router.Router
	supervisor *supervisor.Supervisor
	collector  *proof.Collector
	source     worksource.WorkSource
	poller     *worksource.TaskPoller
	hooks      []string

	// sessions publishes each run to the harness. It is optional: with no
	// registry the workflow behaves exactly as it did before sessions
	// existed, which keeps `ariadne run` usable headlessly.
	sessions *session.Registry
}

// WithSessions attaches a session registry so each run appears in the
// harness alongside every other kind of session.
func (w *Workflow) WithSessions(r *session.Registry) *Workflow {
	w.sessions = r
	return w
}

// beginSession registers a run with the harness and returns it, or nil when
// no registry is attached. Registration failure is never fatal: losing an
// entry in a session list must not abort real work.
func (w *Workflow) beginSession(run *domain.Run, task *domain.Task) *RunSession {
	if w.sessions == nil {
		return nil
	}
	rs := NewRunSession(run, task, repoRoot())
	desc, err := rs.Describe(context.Background())
	if err != nil {
		return nil
	}
	if err := w.sessions.Register(desc, rs); err != nil {
		charmlog.Warn("session registration failed", "run_id", run.ID, "error", err)
		return nil
	}
	return rs
}

// syncSession pushes a run session's current descriptor to the registry so
// the switcher reflects the run without polling.
func (w *Workflow) syncSession(rs *RunSession) {
	if w.sessions == nil || rs == nil {
		return
	}
	if desc, err := rs.Describe(context.Background()); err == nil {
		_ = w.sessions.Update(desc)
	}
}

// New wires a Workflow from a loaded ariadne.toml Config: enabled providers,
// the configured work source, the router, the supervisor, the proof
// collector, and the task poller.
func New(cfg *config.Config) (*Workflow, error) {
	source, err := buildWorkSource(cfg)
	if err != nil {
		return nil, err
	}
	return NewWithSource(cfg, source)
}

// NewWithSource wires a Workflow against a caller-supplied work source. It
// is how the harness runs ad-hoc tasks: a task typed into the interface
// goes through the same routing, worktree isolation, supervision, and proof
// collection as one claimed from an issue tracker, rather than growing a
// second and less careful way to run an agent.
func NewWithSource(cfg *config.Config, source worksource.WorkSource) (*Workflow, error) {
	providers := buildProviders(cfg)
	if len(providers) == 0 {
		return nil, fmt.Errorf("issueworkflow: no enabled providers configured")
	}
	if source == nil {
		return nil, fmt.Errorf("issueworkflow: nil work source")
	}

	rt := router.NewWithPersonas(
		providers,
		cfg.Routing.LabelRoutes,
		cfg.Routing.PersonaRoutes,
		cfg.Personas,
		cfg.Routing.Strategy,
		cfg.Ariadne.DefaultProvider,
	)

	sup := supervisor.New(supervisor.Config{
		WorktreeBaseDir:   cfg.Sandbox.WorktreeDir,
		TimeoutMinutes:    cfg.Sandbox.TimeoutMinutes,
		PreserveOnFailure: cfg.Sandbox.PreserveOnFailure,
		RepoRoot:          repoRoot(),
		WorkflowFile:      cfg.Sandbox.WorkflowFile,
	})

	proofCollector := proof.New(proof.Config{
		RequireCIPass: cfg.Proof.RequireCIPass,
		PRBaseBranch:  cfg.Proof.PRBaseBranch,
	})

	poller := worksource.NewPoller(source, worksource.PollerConfig{
		IntervalSeconds:   cfg.Ariadne.WorkIntervalSeconds,
		MaxConcurrentRuns: cfg.Ariadne.MaxConcurrentRuns,
	})

	return &Workflow{
		router:     rt,
		supervisor: sup,
		collector:  proofCollector,
		source:     source,
		poller:     poller,
		hooks:      cfg.Hooks,
	}, nil
}

// Run starts the polling loop and blocks until ctx is cancelled.
func (w *Workflow) Run(ctx context.Context) error {
	taskCh := w.poller.Run(ctx)

	for task := range taskCh {
		go func() {
			defer w.poller.Done()
			w.executeTask(ctx, task)
		}()
	}

	charmlog.Info("ariadne stopped")
	return nil
}

func (w *Workflow) executeTask(ctx context.Context, task *domain.Task) {
	log := charmlog.With("task_id", task.ID)

	route, err := w.router.Route(task)
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
		w.executeRace(ctx, task, route, log)
		return
	}

	p := route.Providers[0]
	personaName := ""
	if route.Persona != nil {
		personaName = route.Persona.Name
	}
	log.Info("task routed", "title", task.Title, "type", task.Type, "provider", p.Name(), "persona", personaName)
	w.executeRun(ctx, task, p, route.Persona, log)
}

// executeRace spawns N parallel runs and takes the first success.
func (w *Workflow) executeRace(ctx context.Context, task *domain.Task, route router.RouteResult, log *charmlog.Logger) {
	type outcome struct {
		result *supervisor.Result
		p      provider.AgentProvider
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan outcome, len(route.Providers))

	ts := tokenSource(w.source)
	for _, p := range route.Providers {
		go func() {
			run := &domain.Run{ID: newRunID(), TaskID: task.ID, Provider: p.Name()}
			result := w.supervisor.Execute(raceCtx, supervisor.RunRequest{
				Run:          run,
				Task:         task,
				Provider:     p,
				Persona:      route.Persona,
				Source:       w.source,
				ReviewSource: w.source,
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
		w.source.PostResult(ctx, task, fmt.Sprintf("All %d race runs failed", len(route.Providers))) //nolint:errcheck
		return
	}

	log.Info("race winner", "provider", winnerProvider.Name(), "run_id", winner.Run.ID, "failures", failures)
	w.finishRun(ctx, winner.Run, task, winnerProvider, log)
}

func (w *Workflow) executeRun(ctx context.Context, task *domain.Task, p provider.AgentProvider, persona *config.PersonaConfig, log *charmlog.Logger) {
	run := &domain.Run{ID: newRunID(), TaskID: task.ID, Provider: p.Name()}

	rs := w.beginSession(run, task)
	if rs != nil {
		personaName := ""
		if persona != nil {
			personaName = persona.Name
		}
		rs.Started(p.Name(), personaName)
		w.syncSession(rs)
	}

	result := w.supervisor.Execute(ctx, supervisor.RunRequest{
		Run:          run,
		Task:         task,
		Provider:     p,
		Persona:      persona,
		Source:       w.source,
		ReviewSource: w.source,
		TokenSource:  tokenSource(w.source),
	})
	if rs != nil {
		rs.Finished(result.Err)
		w.syncSession(rs)
	}

	if result.Err != nil {
		log.Error("run failed", "run_id", run.ID, "error", result.Err)
		switch task.Type {
		case domain.TaskTypeReview, domain.TaskTypeRevise:
			w.source.RecordReviewOutcome(ctx, task, false, result.Err.Error()) //nolint:errcheck
		case domain.TaskTypeRebase:
			w.source.RecordRebaseOutcome(ctx, task, false, result.Err.Error()) //nolint:errcheck
		default:
			w.source.PostResult(ctx, task, fmt.Sprintf("Run failed: %v", result.Err)) //nolint:errcheck
		}
		return
	}
	switch task.Type {
	case domain.TaskTypeReview, domain.TaskTypeRevise:
		return // outcome already recorded by supervisor
	case domain.TaskTypeRebase:
		return // outcome already recorded by supervisor via RecordRebaseOutcome
	}
	w.finishRun(ctx, run, task, p, log)
}

func (w *Workflow) finishRun(ctx context.Context, run *domain.Run, task *domain.Task, p provider.AgentProvider, log *charmlog.Logger) {
	log.Info("run succeeded", "run_id", run.ID)

	bundle, err := w.collector.Collect(ctx, run, task, p, w.source)
	if err != nil {
		log.Error("proof collection failed", "run_id", run.ID, "error", err)
		w.source.PostResult(ctx, task, fmt.Sprintf("Run succeeded but proof collection failed: %v", err)) //nolint:errcheck
		return
	}

	if err := w.source.PostResult(ctx, task, formatProofSummary(bundle)); err != nil {
		log.Error("post result failed", "error", err)
	}

	// Run post-run hooks with the summary path.
	if len(w.hooks) > 0 {
		summaryPath := filepath.Join(run.WorktreePath, "proof", "summary.json")
		if err := proof.RunHooks(ctx, w.hooks, summaryPath); err != nil {
			log.Warn("post-run hook failed", "run_id", run.ID, "error", err)
		}
	}

	log.Info("run complete — worktree preserved", "run_id", run.ID, "path", run.WorktreePath)
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
		return worksource.NewLinearSource(token, cfg.WorkSources.Linear.TeamID, cfg.WorkSources.Linear.StateFilter)
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
