package policy

import (
	"context"
)

// Engine is the central policy decision point for the gateway.
// It is the generalization of the old Starlark router + future hooks.
//
// The gateway will call into the Engine at key points in the lifecycle
// (routing via SelectRoute, pre-execution via PreRun, post-execution via PostRun).
//
// ResultHandlers (see gateway.ResultHandler) are a separate, lower-level
// delivery mechanism. Policy PostRun is called by the gateway BEFORE any
// registered ResultHandlers, and is intended for policy-level concerns such as:
//   - Recording metrics or learning from outcomes (e.g. updating skill weights).
//   - Emitting structured events for external systems.
//   - Custom side effects that should happen regardless of adapter.
//
// ResultHandlers are for "what to do with the result" from the perspective
// of the originating adapter or system: writing proof artifacts, posting
// to Discord/Signal/webhooks, updating source tickets, etc.
//
// Both are always invoked (additively) for terminal runs in the gateway's
// async completion path. A NoopEngine (the default) makes PostRun a no-op
// so that pure ResultHandler usage requires no policy configuration.
//
// Adapters (MCP, Discord bot, cron poller, CLI, Signal, etc.) submit work
// via Gateway.Submit and do not need to know about PostRun or handlers;
// they can register their own ResultHandlers at construction or runtime
// via RegisterResultHandler if they want per-adapter delivery behavior.
// Future Starlark post_run hooks (Phase 3) will likely be invoked from
// within a StarlarkEngine's PostRun implementation.
type Engine interface {
	// SelectRoute decides which provider/persona (or set for racing) should
	// handle the given invocation. Returning nil means "use gateway defaults".
	SelectRoute(ctx context.Context, inv Invocation) (*RouteDecision, error)

	// PreRun is called after routing but before the executor is invoked.
	// The handler can mutate the invocation (e.g. inject env, rewrite prompt,
	// add extra MCP servers for the agent, or return an error to veto the run).
	PreRun(ctx context.Context, inv *Invocation) error

	// PostRun is called after a run reaches a terminal state (success or failure).
	// It is invoked by the gateway *before* any registered ResultHandlers (see
	// gateway package) and runs additively with them.
	//
	// Use this for policy-driven post-run logic (skill improvement, auditing,
	// cross-run state). Use ResultHandlers for adapter-specific result delivery
	// (proof writing, notifications, callbacks).
	//
	// Implementations must be safe for concurrent calls and must not assume
	// they run on any particular goroutine.
	PostRun(ctx context.Context, run RunSummary, inv Invocation) error
}

// RunSummary is a lightweight view of a completed run passed to PostRun hooks.
type RunSummary struct {
	ID        string
	Title     string
	Provider  string
	Persona   string
	Status    string
	Worktree  string
	Error     string
	Duration  float64 // seconds
	Source    string
	SourceURL string
}

// NoopEngine is a safe default that does nothing (lets the gateway decide everything).
type NoopEngine struct{}

func (NoopEngine) SelectRoute(ctx context.Context, inv Invocation) (*RouteDecision, error) {
	return nil, nil
}

func (NoopEngine) PreRun(ctx context.Context, inv *Invocation) error {
	return nil
}

func (NoopEngine) PostRun(ctx context.Context, run RunSummary, inv Invocation) error {
	return nil
}

// Compile-time interface check
var _ Engine = NoopEngine{}
