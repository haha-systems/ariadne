package provider

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCodexAdapter_Name(t *testing.T) {
	a := NewCodexAdapter("", nil)
	if a.Name() != "codex" {
		t.Errorf("unexpected name: %s", a.Name())
	}
}

func TestCodexAdapter_DefaultBinary(t *testing.T) {
	a := NewCodexAdapter("", nil)
	if a.shell.binary != "codex" {
		t.Errorf("expected binary=codex, got %s", a.shell.binary)
	}
}

func TestCodexAdapter_StripsLegacyExecFromExtraArgs(t *testing.T) {
	a := NewCodexAdapter("", []string{"exec", "--full-auto"})
	if len(a.shell.extraArgs) != 1 || a.shell.extraArgs[0] != "--full-auto" {
		t.Fatalf("unexpected sanitized args: %v", a.shell.extraArgs)
	}
}

func TestGeminiCLIAdapter_DefaultModel(t *testing.T) {
	a := NewGeminiCLIAdapter("", nil, 0)
	if !containsFlag(a.shell.extraArgs, "--model") {
		t.Error("expected --model flag to be injected by default")
	}
	// Should contain gemini-2.5-pro
	found := false
	for _, arg := range a.shell.extraArgs {
		if arg == "gemini-2.5-pro" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected gemini-2.5-pro in args, got %v", a.shell.extraArgs)
	}
}

func TestGeminiCLIAdapter_NoDoubleModel(t *testing.T) {
	// If caller already passes --model, we shouldn't prepend another.
	a := NewGeminiCLIAdapter("", []string{"--model", "gemini-1.0-pro"}, 0)
	count := 0
	for _, arg := range a.shell.extraArgs {
		if arg == "--model" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one --model flag, got %d in %v", count, a.shell.extraArgs)
	}
}

func TestOpenCodeAdapter_Name(t *testing.T) {
	a := NewOpenCodeAdapter("", nil, 0)
	if a.Name() != "opencode" {
		t.Errorf("unexpected name: %s", a.Name())
	}
}

func TestCustomAdapter_Name(t *testing.T) {
	a := NewCustomAdapter("my-agent", "/usr/bin/my-agent", nil, 0)
	if a.Name() != "my-agent" {
		t.Errorf("unexpected name: %s", a.Name())
	}
}

func TestApplyTemplates(t *testing.T) {
	rc := RunContext{
		TaskFile: "/tmp/task.md",
		RepoPath: "/workspace/runs/run_123",
	}
	cases := []struct {
		input, want string
	}{
		{"--task={{task_file}}", "--task=/tmp/task.md"},
		{"--dir={{repo_path}}", "--dir=/workspace/runs/run_123"},
		{"no-template", "no-template"},
	}
	for _, tc := range cases {
		got := applyTemplates(tc.input, rc)
		if got != tc.want {
			t.Errorf("applyTemplates(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestContainsFlag(t *testing.T) {
	args := []string{"--foo", "--bar", "value"}
	if !containsFlag(args, "--foo") {
		t.Error("expected true for --foo")
	}
	if containsFlag(args, "--baz") {
		t.Error("expected false for --baz")
	}
}

func TestCodexLaunchArgs_IncludeSharedGitAndJJDirs(t *testing.T) {
	repo := t.TempDir()
	initCmd := exec.Command("git", "init", "-q", repo)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v: %s", err, out)
	}
	gitDir := filepath.Join(repo, ".git")
	jjDir := filepath.Join(repo, ".jj")
	if err := os.Mkdir(jjDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var err error
	gitDir, err = filepath.EvalSymlinks(gitDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(.git) failed: %v", err)
	}
	jjDir, err = filepath.EvalSymlinks(jjDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(.jj) failed: %v", err)
	}
	args, err := codexLaunchArgs(repo)
	if err != nil {
		t.Fatalf("codexLaunchArgs returned error: %v", err)
	}

	if len(args) != 5 {
		t.Fatalf("unexpected arg count: %v", args)
	}
	if args[0] != "exec" {
		t.Fatalf("expected exec as first arg, got %q", args[0])
	}
	if args[1] != "--add-dir" || args[2] != gitDir {
		t.Fatalf("expected shared git dir in args, got %v", args)
	}
	if args[3] != "--add-dir" || args[4] != jjDir {
		t.Fatalf("expected shared jj dir in args, got %v", args)
	}
}
