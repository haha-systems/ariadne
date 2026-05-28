package policy

import (
	"context"

	"github.com/haha-systems/ariadne/internal/gateway"
)

// Engine is the central policy decision point for the gateway.
// It is the generalization of the old Starlark router + future hooks.
//
// The gateway will call into the Engine at key points in the lifecycle
// (routing, pre-execution, post-execution, etc.).
type Engine interface {
	// SelectRoute decides which provider/persona (or set for racing) should
	// handle the given invocation. Returning nil means "use gateway defaults".
	SelectRoute(ctx context.Context, inv gateway.Invocation) (*RouteDecision, error)

	// PreRun is called after routing but before the executor is invoked.
	// The handler can mutate the invocation (e.g. inject env, rewrite prompt,
	// add extra MCP servers for the agent, or return an error to veto the run).
	PreRun(ctx context.Context, inv *gateway.Invocation) error

	// PostRun is called after a run reaches a terminal state (success or failure).
	// This is the primary extension point for custom result handling, notifications,
	// skill learning, etc. It is called *in addition to* any ResultHandlers
	// registered on the gateway.
	PostRun(ctx context.Context, run *gateway.Run, inv gateway.Invocation) error
}

// RouteDecision is the output of SelectRoute.
type RouteDecision struct {
	Provider string
	Persona  string
	// RaceProviders can be set to run multiple providers in parallel.
	// If non-empty, the gateway may treat this as a race.
	RaceProviders []string
}

// NoopEngine is a safe default that does nothing (lets the gateway decide everything).
type NoopEngine struct{}

func (NoopEngine) SelectRoute(ctx context.Context, inv gateway.Invocation) (*RouteDecision, error) {
	return nil, nil
}

func (NoopEngine) PreRun(ctx context.Context, inv *gateway.Invocation) error {
	return nil
}

func (NoopEngine) PostRun(ctx context.Context, run *gateway.Run, inv gateway.Invocation) error {
	return nil
}

// Compile-time interface check
var _ Engine = NoopEngine{}
