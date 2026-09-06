package provider

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexAdapter_SupportsModelOverride_False(t *testing.T) {
	a := NewCodexAdapter("", nil)
	if a.SupportsModelOverride() {
		t.Error("expected codex to report no per-invocation model override support")
	}
}

func TestCodexAdapter_Run_IgnoresModelOverride(t *testing.T) {
	// Use "echo" as a stand-in for the codex binary. Codex's Run never reads
	// rc.ModelOverride, so setting it must not surface as a bogus flag or
	// otherwise change the invocation.
	a := NewCodexAdapter("echo", []string{"hello"})

	dir := t.TempDir()
	taskFile := filepath.Join(dir, "task.md")
	if err := os.WriteFile(taskFile, []byte("do stuff"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	rc := RunContext{
		RepoPath:      dir,
		TaskFile:      taskFile,
		LogWriter:     &buf,
		ModelOverride: "gpt-6-astra",
	}

	handle, err := a.Run(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := handle.Wait(); err != nil {
		t.Fatalf("wait error: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "gpt-6-astra") {
		t.Errorf("expected ModelOverride to be ignored, but it leaked into the invocation: %q", out)
	}
}
