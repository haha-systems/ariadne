// Package harness is Ariadne's single entry point: one process that runs
// the workflows and one interface that shows all of them.
//
// Before the harness, each workflow had its own command, its own lifetime,
// and its own idea of how you watched it. That is two tools that happen to
// ship in one binary. The harness owns the session registry, starts the
// workflows that feed it, and hands the interface a Host — so "what is
// Ariadne doing right now" has exactly one answer and one place to look.
//
// It starts work and it observes work. It never executes an agent itself:
// the supervisor and the research runtime remain solely responsible for
// spending quota, which is what keeps the interface unable to do anything
// the CLI cannot.
package harness

import (
	"context"
	"fmt"
	"sync"

	charmlog "github.com/charmbracelet/log"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/issueworkflow"
	"github.com/haha-systems/ariadne/internal/session"
	"github.com/haha-systems/ariadne/internal/worksource"
)

// Harness owns the session registry and the workflows registering into it.
type Harness struct {
	cfg      *config.Config
	registry *session.Registry

	mu      sync.Mutex
	started bool
	// local is the in-process source backing tasks typed into the
	// interface, when a local lane has been started.
	local *worksource.Local
	wg    sync.WaitGroup

	// runErr records the first workflow failure, reported when the harness
	// is closed. A workflow dying must not take the interface down with
	// it: the sessions it already produced are still worth looking at.
	runErr error
}

// Compile-time proof the harness is both a session set and a way to start
// another one.
var (
	_ session.Host    = (*Harness)(nil)
	_ session.Starter = (*Harness)(nil)
)

// New constructs a harness from a loaded config.
func New(cfg *config.Config) (*Harness, error) {
	if cfg == nil {
		return nil, fmt.Errorf("harness: nil config")
	}
	return &Harness{cfg: cfg, registry: session.NewRegistry()}, nil
}

// Host is the session set the interface renders. The harness returns
// itself rather than the bare registry: it is both the set of sessions and
// the thing that can start another, and handing over only the registry
// would silently strip the interface's ability to begin work.
func (h *Harness) Host() session.Host { return h }

// List implements session.Host.
func (h *Harness) List(ctx context.Context) ([]session.Descriptor, error) {
	return h.registry.List(ctx)
}

// Open implements session.Host.
func (h *Harness) Open(ctx context.Context, id string) (session.Controller, error) {
	return h.registry.Open(ctx, id)
}

// Watch implements session.Host.
func (h *Harness) Watch(ctx context.Context) (<-chan session.Change, error) {
	return h.registry.Watch(ctx)
}

// Registry exposes the concrete registry so a workflow started outside the
// harness — a research programme launched by its own command, say — can
// register itself into the same list.
func (h *Harness) Registry() *session.Registry { return h.registry }

// StartIssueWorkflow begins polling the configured work source, publishing
// each run as a session. It returns nil and logs when no work source is
// configured: a harness with nothing to poll is a perfectly good harness
// for watching a research programme, and refusing to start would be wrong.
func (h *Harness) StartIssueWorkflow(ctx context.Context) error {
	h.mu.Lock()
	if h.started {
		h.mu.Unlock()
		return fmt.Errorf("harness: issue workflow already started")
	}
	h.started = true
	h.mu.Unlock()

	wf, err := issueworkflow.New(h.cfg)
	if err != nil {
		// A missing work source or provider is a configuration state, not
		// a crash: report it and let the harness run without it.
		charmlog.Warn("issue workflow not started", "reason", err)
		return nil
	}
	wf.WithSessions(h.registry)

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		if err := wf.Run(ctx); err != nil && ctx.Err() == nil {
			charmlog.Error("issue workflow stopped", "error", err)
			h.mu.Lock()
			if h.runErr == nil {
				h.runErr = err
			}
			h.mu.Unlock()
		}
	}()
	return nil
}

// AddSession registers an already-running session, so work started by
// another command appears in the same list.
func (h *Harness) AddSession(desc session.Descriptor, controller session.Controller) error {
	return h.registry.Register(desc, controller)
}

// Close stops observing and reports the first workflow failure. It does not
// stop running agents: the contexts their workflows were started with do
// that, and a harness that killed live work on exit would make detaching
// impossible.
func (h *Harness) Close() error {
	h.wg.Wait()
	h.registry.Close()

	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runErr
}

// StartLocalLane starts a second workflow over an in-process work source,
// so tasks typed into the interface run through the same routing, worktree
// isolation, supervision, and proof collection as one claimed from an issue
// tracker. Without it, "start work from the harness" would mean a second
// and less careful way to run an agent.
//
// It must be called before StartTask.
func (h *Harness) StartLocalLane(ctx context.Context) error {
	h.mu.Lock()
	if h.local != nil {
		h.mu.Unlock()
		return nil
	}
	local := worksource.NewLocal(nil)
	h.local = local
	h.mu.Unlock()

	// A local lane polls promptly: a person who just typed a task is
	// waiting for it, unlike an issue that has been sitting in a tracker.
	cfg := *h.cfg
	cfg.Ariadne.WorkIntervalSeconds = localPollSeconds

	wf, err := issueworkflow.NewWithSource(&cfg, local)
	if err != nil {
		return fmt.Errorf("harness: starting local lane: %w", err)
	}
	wf.WithSessions(h.registry)

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		if err := wf.Run(ctx); err != nil && ctx.Err() == nil {
			charmlog.Error("local lane stopped", "error", err)
		}
	}()
	return nil
}

// localPollSeconds is how long a task typed into the interface waits before
// the lane picks it up.
const localPollSeconds = 1

// StartTask queues an ad-hoc task. It returns the task's title; the run
// appears in the session list once the lane claims it, which is why this
// returns promptly rather than blocking until a session exists.
//
// The harness queues work; it never executes it. The supervisor remains the
// only thing that spends an agent invocation.
func (h *Harness) StartTask(_ context.Context, prompt string) (string, error) {
	h.mu.Lock()
	local := h.local
	h.mu.Unlock()

	if local == nil {
		return "", fmt.Errorf("harness: no local lane — start one before submitting a task")
	}
	task, err := local.Submit(prompt)
	if err != nil {
		return "", err
	}
	return task.Title, nil
}
