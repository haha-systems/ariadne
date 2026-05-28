package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/haha-systems/ariadne/internal/domain"
)

// gateway is the concrete implementation of Gateway for the spike.
// It is intentionally minimal: it accepts invocations, does trivial routing
// (or calls Starlark later), and delegates execution to an Executor.
// This proves the seam before we do the big extraction from supervisor/operator.
type gateway struct {
	cfg Config

	mu        sync.Mutex
	runs      map[string]*Run
	active    map[string]context.CancelFunc // for cancellation
	handlers  []ResultHandler
	executor  Executor // the thing that actually runs the agent (injected for testability)
}

// Executor is the narrow seam for actually running an agent.
// The gateway owns all mutation of Run records for thread safety.
// Later this becomes the clean internal engine.
type Executor interface {
	// Execute runs the invocation to completion (or until ctx is cancelled).
	// It must be safe for concurrent calls with different runIDs.
	// The executor must NOT mutate any shared Run records — only return
	// the worktree path it created (if any) and an error.
	Execute(ctx context.Context, runID string, inv Invocation) (worktree string, err error)
}

// New creates a Gateway. For the spike we require an Executor to be supplied
// (this keeps the first cut tiny and testable).
func New(cfg Config, exec Executor) (Gateway, error) {
	if exec == nil {
		return nil, fmt.Errorf("gateway: executor is required")
	}

	handlers := append([]ResultHandler(nil), cfg.ResultHandlers...)

	// Automatically register a logging handler unless the caller explicitly
	// passed a no-op or disabled it. This makes adapters and direct usage
	// get reasonable observability out of the box.
	hasHandler := false
	for _, h := range handlers {
		if _, ok := h.(noopResultHandler); ok {
			hasHandler = true
			break
		}
	}
	if !hasHandler {
		handlers = append(handlers, NewLoggingResultHandler(nil))
	}

	g := &gateway{
		cfg:      cfg,
		runs:     make(map[string]*Run),
		active:   make(map[string]context.CancelFunc),
		handlers: handlers,
		executor: exec,
	}
	return g, nil
}

func (g *gateway) Submit(ctx context.Context, inv Invocation) (*Run, error) {
	if inv.ID == "" {
		inv.ID = fmt.Sprintf("run_%d", time.Now().UnixNano())
	}
	if inv.Title == "" {
		inv.Title = inv.ID
	}

	run := &Run{
		ID:        inv.ID,
		Title:     inv.Title,
		Status:    domain.RunStatusPending,
		StartedAt: time.Now().UTC(),
		Metadata:  copyMap(inv.Metadata),
	}

	// Trivial "routing" for the spike (real Starlark policy comes later).
	// If no explicit provider, fall back to config default.
	if inv.Provider == "" {
		inv.Provider = g.cfg.DefaultProvider
	}
	run.Provider = inv.Provider
	run.Persona = inv.Persona

	g.mu.Lock()
	g.runs[run.ID] = run
	g.mu.Unlock()

	// Launch asynchronously so Submit returns immediately (like the real paths do today).
	runCtx, cancel := context.WithCancel(context.Background())
	g.mu.Lock()
	g.active[run.ID] = cancel
	g.mu.Unlock()

	go func() {
		defer func() {
			g.mu.Lock()
			delete(g.active, run.ID)
			g.mu.Unlock()
			cancel()
		}()

		g.updateRun(run.ID, func(r *Run) {
			r.Status = domain.RunStatusRunning
			r.LastEvent = "gateway_submitted"
		})

		worktree, execErr := g.executor.Execute(runCtx, run.ID, inv)

		g.updateRun(run.ID, func(r *Run) {
			if worktree != "" {
				r.Worktree = worktree
			}
			if execErr != nil {
				r.Status = domain.RunStatusFailed
				r.LastError = execErr.Error()
				r.LastEvent = "execution_failed"
			} else if r.Status == domain.RunStatusRunning {
				r.Status = domain.RunStatusSucceeded
				r.LastEvent = "execution_succeeded"
			}
			finished := time.Now().UTC()
			r.FinishedAt = &finished
		})

		// Snapshot for handlers (they must not mutate the live record).
		snapshot := g.copyRun(run.ID)
		if snapshot != nil {
			for _, h := range g.handlers {
				_ = h.Handle(context.Background(), snapshot, &inv, nil)
			}
		}
	}()

	// Return a copy so callers never hold a reference that the background goroutine mutates.
	return g.copyRun(run.ID), nil
}

func (g *gateway) Cancel(runID string) (bool, error) {
	g.mu.Lock()
	cancel, ok := g.active[runID]
	g.mu.Unlock()

	if ok {
		cancel()
		g.updateRun(runID, func(r *Run) {
			r.LastEvent = "cancel_requested"
		})
		return true, nil
	}

	// Not active — check if we even know about it.
	g.mu.Lock()
	r, exists := g.runs[runID]
	g.mu.Unlock()
	if exists && r.Status != domain.RunStatusRunning {
		return false, nil // already terminal
	}
	return false, fmt.Errorf("run %s is not known or not active", runID)
}

func (g *gateway) GetRun(runID string) (*Run, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	r, ok := g.runs[runID]
	if !ok {
		return nil, fmt.Errorf("run %s not found", runID)
	}
	// Return a shallow copy to avoid external mutation races.
	cp := *r
	return &cp, nil
}

func (g *gateway) ListRuns(limit int) ([]*Run, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	out := make([]*Run, 0, len(g.runs))
	for _, r := range g.runs {
		cp := *r
		out = append(out, &cp)
	}
	// Naive: newest first by StartedAt for the spike.
	// Real implementation will have better indexing.
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (g *gateway) RegisterResultHandler(h ResultHandler) {
	if h == nil {
		return
	}
	g.mu.Lock()
	g.handlers = append(g.handlers, h)
	g.mu.Unlock()
}

func (g *gateway) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, cancel := range g.active {
		cancel()
	}
	g.active = make(map[string]context.CancelFunc)
	return nil
}

// updateRun applies a mutation to the live Run record under the lock.
func (g *gateway) updateRun(runID string, mutate func(*Run)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r, ok := g.runs[runID]; ok {
		mutate(r)
	}
}

// copyRun returns a shallow copy of the Run (or nil if not found), under the lock.
func (g *gateway) copyRun(runID string) *Run {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r, ok := g.runs[runID]; ok {
		cp := *r
		return &cp
	}
	return nil
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}