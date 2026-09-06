package workspace

import (
	"context"
	"fmt"
	"os"
)

// EphemeralProvider always returns a plain temp directory, regardless of
// the requested Kind. It never shells out to git, so it's the type that
// makes "don't require a worktree for every thought" true in practice —
// use it (directly, or via Router) for invocations that only need scratch
// disk space.
type EphemeralProvider struct{}

func (EphemeralProvider) Acquire(context.Context, Kind) (Workspace, error) {
	dir, err := os.MkdirTemp("", "ariadne-ephemeral-*")
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: create ephemeral dir: %w", err)
	}
	return Workspace{Kind: KindEphemeral, Path: dir}, nil
}

func (EphemeralProvider) Release(_ context.Context, ws Workspace) error {
	return os.RemoveAll(ws.Path)
}
