package workspace

import "context"

// Router composes a GitProvider and an EphemeralProvider behind one
// Provider, dispatching on the requested (or released) Kind. It's the
// Provider most callers should hold: they don't need to know which
// underlying implementation handles which Kind.
type Router struct {
	Git       *GitProvider
	Ephemeral *EphemeralProvider
}

func (r *Router) Acquire(ctx context.Context, kind Kind) (Workspace, error) {
	if kind == KindEphemeral {
		return r.Ephemeral.Acquire(ctx, kind)
	}
	return r.Git.Acquire(ctx, kind)
}

func (r *Router) Release(ctx context.Context, ws Workspace) error {
	if ws.Kind == KindEphemeral {
		return r.Ephemeral.Release(ctx, ws)
	}
	return r.Git.Release(ctx, ws)
}
