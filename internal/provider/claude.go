package provider

import "context"

// ClaudeCodeAdapter invokes the `claude` CLI.
type ClaudeCodeAdapter struct{ shell shellAdapter }

func NewClaudeCodeAdapter(binary string, extraArgs []string, costPer1kTokens float64) *ClaudeCodeAdapter {
	if binary == "" {
		binary = "claude"
	}
	return &ClaudeCodeAdapter{shell: shellAdapter{
		name:            "claude",
		binary:          binary,
		extraArgs:       extraArgs,
		costPer1kTokens: costPer1kTokens,
		capabilities:    []Capability{CapabilityFileEdit, CapabilityBash},
	}}
}

func (a *ClaudeCodeAdapter) Name() string                                    { return a.shell.adapterName() }
func (a *ClaudeCodeAdapter) Capabilities() []Capability                     { return a.shell.adapterCapabilities() }
func (a *ClaudeCodeAdapter) CostEstimate(n int) (float64, bool)             { return a.shell.adapterCostEstimate(n) }
func (a *ClaudeCodeAdapter) Run(ctx context.Context, rc RunContext) (RunHandle, error) {
	return a.shell.adapterRun(ctx, rc, "--print", "--no-interactive")
}
