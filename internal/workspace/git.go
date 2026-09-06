package workspace

import (
	"context"
	"fmt"
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
