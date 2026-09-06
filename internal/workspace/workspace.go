// Package workspace generalizes Ariadne's isolated git worktree strategy
// into a Provider abstraction that distinguishes invocations that need real
// repository access from ones that don't. Not every cognitive invocation
// should pay for a git worktree: interpreting a result or designing an
// experiment needs no repo at all, while implementing or running one does.
package workspace

import "context"

// Kind distinguishes an ephemeral scratch workspace from one backed by an
// isolated git worktree.
type Kind string

const (
	// KindEphemeral is a plain temp directory with no git history — for
	// invocations that never touch the repository.
	KindEphemeral Kind = "ephemeral"
	// KindWorktreeBacked is an isolated git worktree — for invocations that
	// read or modify repository state.
	KindWorktreeBacked Kind = "worktree_backed"
)

// Workspace is a filesystem location an invocation may use.
type Workspace struct {
	Kind Kind
	Path string
}

// Provider acquires and releases Workspaces.
type Provider interface {
	Acquire(ctx context.Context, kind Kind) (Workspace, error)
	Release(ctx context.Context, ws Workspace) error
}
