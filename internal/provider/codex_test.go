package provider

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// catStdinScript returns the path to an executable shell script that
// ignores its arguments and cats stdin — useful for asserting on piped
// input when the code under test always prepends its own fixed argv (which
// would break plain "cat").
func catStdinScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cat-stdin.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncat\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexAdapter_SupportsModelOverride_True(t *testing.T) {
	a := NewCodexAdapter("", nil)
	if !a.SupportsModelOverride() {
		t.Error("expected codex to report per-invocation model override support (codex exec accepts -m/--model)")
	}
}

func TestCodexAdapter_Run_InvalidTaskFile(t *testing.T) {
	a := NewCodexAdapter("echo", nil)
	_, err := a.Run(context.Background(), RunContext{RepoPath: t.TempDir(), TaskFile: "/does/not/exist"})
	if err == nil {
		t.Error("expected error for missing task file")
	}
}

func TestCodexAdapter_Run_UsesExecSubcommandAndPipesTaskFile(t *testing.T) {
	// Use a stand-in binary that ignores its argv and echoes stdin, so we
	// can assert the task file content is actually piped in regardless of
	// the "exec"/"--skip-git-repo-check" args CodexAdapter always prepends
	// (plain "cat" would instead try to open those as filenames and fail).
	// This guards against the exact bug being fixed here: bare `codex`,
	// with no `exec` subcommand and no stdin, launches an interactive TUI
	// that fails outside a real terminal.
	a := NewCodexAdapter(catStdinScript(t), nil)

	dir := t.TempDir()
	taskFile := filepath.Join(dir, "task.md")
	if err := os.WriteFile(taskFile, []byte("do the research task"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	rc := RunContext{RepoPath: dir, TaskFile: taskFile, LogWriter: &buf}

	handle, err := a.Run(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := handle.Wait(); err != nil {
		t.Fatalf("wait error: %v", err)
	}

	if !strings.Contains(buf.String(), "do the research task") {
		t.Errorf("expected the task file contents to be piped via stdin, got %q", buf.String())
	}
}

func TestCodexAdapter_Run_AppendsExecAndSkipGitRepoCheckArgs(t *testing.T) {
	// "echo" ignores stdin and just prints its args, letting us assert on
	// the argument list codex is actually invoked with.
	a := NewCodexAdapter("echo", []string{"--dangerously-bypass-approvals-and-sandbox"})

	dir := t.TempDir()
	taskFile := filepath.Join(dir, "task.md")
	if err := os.WriteFile(taskFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	handle, err := a.Run(context.Background(), RunContext{RepoPath: dir, TaskFile: taskFile, LogWriter: &buf})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := handle.Wait(); err != nil {
		t.Fatalf("wait error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected invocation to include %q, got %q", want, out)
		}
	}
}

func TestCodexAdapter_Run_AppendsModelFlag(t *testing.T) {
	a := NewCodexAdapter("echo", nil)

	dir := t.TempDir()
	taskFile := filepath.Join(dir, "task.md")
	if err := os.WriteFile(taskFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	rc := RunContext{RepoPath: dir, TaskFile: taskFile, LogWriter: &buf, ModelOverride: "gpt-6-astra"}

	handle, err := a.Run(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := handle.Wait(); err != nil {
		t.Fatalf("wait error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "--model") || !strings.Contains(out, "gpt-6-astra") {
		t.Errorf("expected --model gpt-6-astra in invocation, got %q", out)
	}
}
