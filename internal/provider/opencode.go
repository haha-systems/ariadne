package provider

import "context"

// OpenCodeAdapter invokes the `opencode` binary (sst/opencode).
type OpenCodeAdapter struct{ shell shellAdapter }

func NewOpenCodeAdapter(binary string, extraArgs []string, costPer1kTokens float64) *OpenCodeAdapter {
	if binary == "" {
		binary = "opencode"
	}
	return &OpenCodeAdapter{shell: shellAdapter{
		name:            "opencode",
		binary:          binary,
		extraArgs:       extraArgs,
		costPer1kTokens: costPer1kTokens,
		capabilities:    []Capability{CapabilityFileEdit, CapabilityBash},
	}}
}

func (a *OpenCodeAdapter) Name() string                                    { return a.shell.adapterName() }
func (a *OpenCodeAdapter) Capabilities() []Capability                     { return a.shell.adapterCapabilities() }
func (a *OpenCodeAdapter) CostEstimate(n int) (float64, bool)             { return a.shell.adapterCostEstimate(n) }
func (a *OpenCodeAdapter) Run(ctx context.Context, rc RunContext) (RunHandle, error) {
	return a.shell.adapterRun(ctx, rc)
}
