package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// GitProvider acquires isolated git worktrees for KindWorktreeBacked
// requests. It only ever produces worktree-backed workspaces — pair it with
// EphemeralProvider behind a Router for callers that need both kinds.
type GitProvider struct {
	// RepoRoot is the git repository worktrees are created against.
	RepoRoot string
	// BaseDir is where new worktree directories are created, relative to
	// RepoRoot (e.g. ".ariadne/research").
	BaseDir string
}

func (p *GitProvider) Acquire(ctx context.Context, _ Kind) (Workspace, error) {
	path := filepath.Join(p.RepoRoot, p.BaseDir, newWorkspaceID())
	if err := createWorktree(ctx, p.RepoRoot, path); err != nil {
		return Workspace{}, fmt.Errorf("workspace: create worktree: %w", err)
	}
	return Workspace{Kind: KindWorktreeBacked, Path: path}, nil
}

func (p *GitProvider) Release(ctx context.Context, ws Workspace) error {
	if err := removeWorktree(ctx, p.RepoRoot, ws.Path); err != nil {
		return fmt.Errorf("workspace: remove worktree: %w", err)
	}
	return nil
}

// PersistentGitProvider acquires a single, fixed-path worktree and reuses
// it across calls instead of creating a fresh one each time. Unlike
// GitProvider (one isolated worktree per run, torn down on Release), this
// is for long-lived work — e.g. a research project — where an agent's
// uncommitted changes and experiment artifacts need to survive between
// separate `ariadne research run` invocations. Release is deliberately a
// no-op: the worktree is only ever removed explicitly, never automatically.
type PersistentGitProvider struct {
	// RepoRoot is the git repository the worktree is created against.
	RepoRoot string
	// Path is the worktree's location — absolute, or relative to RepoRoot.
	Path string
}

func (p *PersistentGitProvider) resolvedPath() string {
	if filepath.IsAbs(p.Path) {
		return p.Path
	}
	return filepath.Join(p.RepoRoot, p.Path)
}

// Acquire reuses the worktree at Path if it already exists, or creates it
// (detached at the current HEAD) on first use.
func (p *PersistentGitProvider) Acquire(ctx context.Context, _ Kind) (Workspace, error) {
	path := p.resolvedPath()
	if _, err := os.Stat(path); err == nil {
		return Workspace{Kind: KindWorktreeBacked, Path: path}, nil
	}
	if err := createWorktree(ctx, p.RepoRoot, path); err != nil {
		return Workspace{}, fmt.Errorf("workspace: create persistent worktree: %w", err)
	}
	return Workspace{Kind: KindWorktreeBacked, Path: path}, nil
}

// Release is a no-op — a persistent worktree is not torn down when a
// session ends.
func (p *PersistentGitProvider) Release(context.Context, Workspace) error { return nil }

func createWorktree(ctx context.Context, repoRoot, path string) error {
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", path)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func removeWorktree(ctx context.Context, repoRoot, path string) error {
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", path)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func newWorkspaceID() string {
	return fmt.Sprintf("ws_%d", time.Now().UnixNano())
}
