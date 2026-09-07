package provider

import (
	"context"
	"fmt"
	"os"
)

// CodexAdapter invokes the `codex` CLI's non-interactive `exec` subcommand.
// It reads `.codex/` config from the repo root inside the worktree.
type CodexAdapter struct{ shell shellAdapter }

func NewCodexAdapter(binary string, extraArgs []string) *CodexAdapter {
	if binary == "" {
		binary = "codex"
	}
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

// SupportsModelOverride reports true: `codex exec` accepts -m/--model.
func (a *CodexAdapter) SupportsModelOverride() bool { return true }

// Run pipes the task file into `codex exec` via stdin, matching
// ClaudeCodeAdapter's approach. Bare `codex` (no subcommand) launches an
// interactive TUI that requires a real terminal and fails with "stdin is
// not a terminal" when run as a subprocess — `exec` is codex's documented
// non-interactive mode. --skip-git-repo-check lets this run in workspaces
// that aren't git repositories (e.g. the research workflow's ephemeral
// workspaces), which is a no-op in a real worktree.
func (a *CodexAdapter) Run(ctx context.Context, rc RunContext) (RunHandle, error) {
	f, err := os.Open(rc.TaskFile)
	if err != nil {
		return nil, fmt.Errorf("codex: open task file: %w", err)
	}
	args := []string{"exec", "--skip-git-repo-check"}
	if rc.ModelOverride != "" {
		args = append(args, "--model", rc.ModelOverride)
	}
	return a.shell.adapterRunWithStdin(ctx, rc, f, args...)
}
