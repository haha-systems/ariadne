package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "init@test.com"},
		{"config", "user.name", "Init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestGitProvider_AcquireRelease_CreatesAndRemovesWorktree(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	p := &GitProvider{RepoRoot: repoRoot, BaseDir: ".ariadne/research"}
	ws, err := p.Acquire(context.Background(), KindWorktreeBacked)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if ws.Kind != KindWorktreeBacked {
		t.Errorf("Kind = %q, want %q", ws.Kind, KindWorktreeBacked)
	}
	if _, err := os.Stat(ws.Path); err != nil {
		t.Fatalf("expected worktree dir to exist: %v", err)
	}
	// A worktree is a real git repo — .git should exist as a file (pointing
	// at the parent's git dir) or directory.
	if _, err := os.Stat(filepath.Join(ws.Path, ".git")); err != nil {
		t.Errorf("expected .git to exist in the worktree: %v", err)
	}

	if err := p.Release(context.Background(), ws); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Errorf("expected worktree dir to be removed after Release(), stat err = %v", err)
	}
}

func TestPersistentGitProvider_ReusesExistingWorktreeAcrossCalls(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	p := &PersistentGitProvider{RepoRoot: repoRoot, Path: filepath.Join(".ariadne", "research", "demo", "workspace")}

	first, err := p.Acquire(context.Background(), KindWorktreeBacked)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if first.Kind != KindWorktreeBacked {
		t.Errorf("Kind = %q, want %q", first.Kind, KindWorktreeBacked)
	}

	// Simulate uncommitted work an agent left behind in a previous session.
	marker := filepath.Join(first.Path, "experiment-notes.md")
	if err := os.WriteFile(marker, []byte("progress so far"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	second, err := p.Acquire(context.Background(), KindWorktreeBacked)
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if second.Path != first.Path {
		t.Errorf("second Acquire() path = %q, want the same path as the first (%q)", second.Path, first.Path)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("expected the previous session's file to survive a second Acquire(): %v", err)
	}
}

func TestPersistentGitProvider_ReleaseIsANoOp(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	p := &PersistentGitProvider{RepoRoot: repoRoot, Path: filepath.Join(".ariadne", "research", "demo", "workspace")}
	ws, err := p.Acquire(context.Background(), KindWorktreeBacked)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if err := p.Release(context.Background(), ws); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(ws.Path); err != nil {
		t.Errorf("expected the worktree to survive Release(), got stat error: %v", err)
	}
}

func TestGitProvider_Acquire_FailsOutsideGitRepo(t *testing.T) {
	p := &GitProvider{RepoRoot: t.TempDir(), BaseDir: ".ariadne/research"}
	if _, err := p.Acquire(context.Background(), KindWorktreeBacked); err == nil {
		t.Fatal("expected an error acquiring a worktree outside a git repo")
	}
}

func TestEphemeralProvider_NeverShellsToGit(t *testing.T) {
	// Point RepoRoot-like state at a directory that is NOT a git repo. If
	// EphemeralProvider ever shelled to git, Acquire would fail here.
	p := EphemeralProvider{}
	ws, err := p.Acquire(context.Background(), KindWorktreeBacked) // even if asked for worktree-backed
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if ws.Kind != KindEphemeral {
		t.Errorf("Kind = %q, want %q (EphemeralProvider always produces ephemeral workspaces)", ws.Kind, KindEphemeral)
	}
	if _, err := os.Stat(ws.Path); err != nil {
		t.Fatalf("expected tempdir to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Path, ".git")); !os.IsNotExist(err) {
		t.Error("expected no .git directory in an ephemeral workspace")
	}

	if err := p.Release(context.Background(), ws); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Error("expected tempdir to be removed after Release()")
	}
}

func TestRouter_DispatchesOnKind(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	r := &Router{
		Git:       &GitProvider{RepoRoot: repoRoot, BaseDir: ".ariadne/research"},
		Ephemeral: &EphemeralProvider{},
	}

	ephemeral, err := r.Acquire(context.Background(), KindEphemeral)
	if err != nil {
		t.Fatalf("Acquire(ephemeral) error = %v", err)
	}
	if ephemeral.Kind != KindEphemeral {
		t.Errorf("expected ephemeral workspace, got %+v", ephemeral)
	}

	worktree, err := r.Acquire(context.Background(), KindWorktreeBacked)
	if err != nil {
		t.Fatalf("Acquire(worktree_backed) error = %v", err)
	}
	if worktree.Kind != KindWorktreeBacked {
		t.Errorf("expected worktree-backed workspace, got %+v", worktree)
	}

	if err := r.Release(context.Background(), ephemeral); err != nil {
		t.Errorf("Release(ephemeral) error = %v", err)
	}
	if err := r.Release(context.Background(), worktree); err != nil {
		t.Errorf("Release(worktree) error = %v", err)
	}
}
