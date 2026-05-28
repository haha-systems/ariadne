package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CodexAdapter invokes the `codex` CLI.
// It reads `.codex/` config from the repo root inside the worktree.
type CodexAdapter struct{ shell shellAdapter }

func NewCodexAdapter(binary string, extraArgs []string) *CodexAdapter {
	if binary == "" {
		binary = "codex"
	}
	extraArgs = sanitizeCodexExtraArgs(extraArgs)
	return &CodexAdapter{shell: shellAdapter{
		name:         "codex",
		binary:       binary,
		extraArgs:    extraArgs,
		capabilities: []Capability{CapabilityFileEdit, CapabilityBash},
	}}
}

func (a *CodexAdapter) Name() string                       { return a.shell.adapterName() }
func (a *CodexAdapter) Capabilities() []Capability         { return a.shell.adapterCapabilities() }
func (a *CodexAdapter) CostEstimate(n int) (float64, bool) { return a.shell.adapterCostEstimate(n) }
func (a *CodexAdapter) Run(ctx context.Context, rc RunContext) (RunHandle, error) {
	f, err := os.Open(rc.TaskFile)
	if err != nil {
		return nil, fmt.Errorf("codex: open task file: %w", err)
	}
	args, err := codexLaunchArgs(rc.RepoPath)
	if err != nil {
		f.Close()
		return nil, err
	}
	return a.shell.adapterRunWithStdin(ctx, rc, f, args...)
}

func codexLaunchArgs(repoPath string) ([]string, error) {
	args := []string{"exec"}
	for _, dir := range codexWritableDirs(repoPath) {
		args = append(args, "--add-dir", dir)
	}
	return args, nil
}

func codexWritableDirs(repoPath string) []string {
	dirs := make([]string, 0, 2)

	commonDir, err := gitOutput(repoPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return dirs
	}
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return dirs
	}

	dirs = append(dirs, commonDir)
	if filepath.Base(commonDir) != ".git" {
		return dirs
	}

	jjDir := filepath.Join(filepath.Dir(commonDir), ".jj")
	if info, err := os.Stat(jjDir); err == nil && info.IsDir() {
		dirs = append(dirs, jjDir)
	}

	return dirs
}

func gitOutput(repoPath string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("codex: git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func sanitizeCodexExtraArgs(args []string) []string {
	out := append([]string(nil), args...)
	for len(out) > 0 && out[0] == "exec" {
		out = out[1:]
	}
	return out
}
