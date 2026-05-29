package policy

import (
	"context"
)

// Engine is the central policy decision point for the gateway.
// It is the generalization of the old Starlark router + future hooks.
//
// The gateway will call into the Engine at key points in the lifecycle
// (routing, pre-execution, post-execution, etc.).
//
// Implementations (e.g. StarlarkEngine) are the primary extension mechanism
// for routing, pre/post behaviors, and (future) scheduling across adapters.
type Engine interface {
	// SelectRoute decides which provider/persona (or set for racing) should
	// handle the given invocation. Returning nil means "use gateway defaults".
	SelectRoute(ctx context.Context, inv Invocation) (*RouteDecision, error)

	// PreRun is called after SelectRoute (and any decision application) but
	// before the executor is invoked.
	//
	// The handler can mutate the invocation (e.g. inject env vars via Env,
	// rewrite Prompt, override Provider/Persona, mutate Labels/Metadata,
	// or return an error to veto the run entirely).
	//
	// The gateway applies supported mutations before persisting the run
	// record and launching the executor. See StarlarkEngine for the concrete
	// host API and safety model when using Starlark policies.
	PreRun(ctx context.Context, inv *Invocation) error

	// PostRun is called after a run reaches a terminal state (success or failure).
	// This is the primary extension point for custom result handling, notifications,
	// skill learning, etc. It is called *in addition to* any ResultHandlers
	// registered on the gateway.
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
