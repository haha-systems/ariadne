package provider

import "context"

// CodexAdapter invokes the `codex` CLI.
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

func (a *CodexAdapter) Name() string                                    { return a.shell.adapterName() }
func (a *CodexAdapter) Capabilities() []Capability                     { return a.shell.adapterCapabilities() }
func (a *CodexAdapter) CostEstimate(n int) (float64, bool)             { return a.shell.adapterCostEstimate(n) }
func (a *CodexAdapter) Run(ctx context.Context, rc RunContext) (RunHandle, error) {
	return a.shell.adapterRun(ctx, rc)
}
