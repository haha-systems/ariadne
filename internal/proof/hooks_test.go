package proof

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunHooks_PathPassedAsArg verifies that the summary path is passed as $1
// to the hook script, not interpolated into the shell command string.
func TestRunHooks_PathPassedAsArg(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "out.txt")
	// The hook uses $1 to access the summary path argument.
	hook := "echo \"$1\" > " + outFile
	summaryPath := "/tmp/proof/summary.json"

	if err := RunHooks(context.Background(), []string{hook}, summaryPath); err != nil {
		t.Fatalf("RunHooks: %v", err)
	}

	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if strings.TrimSpace(string(got)) != summaryPath {
		t.Errorf("hook received %q via $1, want %q", strings.TrimSpace(string(got)), summaryPath)
	}
}

// TestRunHooks_PathWithSpaces verifies that a summary path containing spaces
// is passed correctly as $1 without word-splitting or quoting issues.
func TestRunHooks_PathWithSpaces(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "out.txt")
	hook := "echo \"$1\" > " + outFile
	summaryPath := "/tmp/run 1/proof/summary.json"

	if err := RunHooks(context.Background(), []string{hook}, summaryPath); err != nil {
		t.Fatalf("RunHooks: %v", err)
	}

	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if strings.TrimSpace(string(got)) != summaryPath {
		t.Errorf("hook received %q via $1, want %q", strings.TrimSpace(string(got)), summaryPath)
	}
}

// TestRunHooks_HookError verifies that a failing hook returns an error.
func TestRunHooks_HookError(t *testing.T) {
	err := RunHooks(context.Background(), []string{"exit 1"}, "/tmp/summary.json")
	if err == nil {
		t.Error("expected error from failing hook, got nil")
	}
}

// TestRunHooks_Empty verifies that an empty hook list is a no-op.
func TestRunHooks_Empty(t *testing.T) {
	if err := RunHooks(context.Background(), nil, "/tmp/summary.json"); err != nil {
		t.Errorf("expected no error for empty hooks, got: %v", err)
	}
}
