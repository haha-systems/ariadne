package provider

import "context"

// GeminiCLIAdapter invokes Google's `gemini` CLI.
type GeminiCLIAdapter struct{ shell shellAdapter }

func NewGeminiCLIAdapter(binary string, extraArgs []string, costPer1kTokens float64) *GeminiCLIAdapter {
	if binary == "" {
		binary = "gemini"
	}
	// Default to gemini-2.5-pro unless the caller supplies a --model override.
	if !containsFlag(extraArgs, "--model") {
		extraArgs = append([]string{"--model", "gemini-2.5-pro"}, extraArgs...)
	}
	return &GeminiCLIAdapter{shell: shellAdapter{
		name:            "gemini",
		binary:          binary,
		extraArgs:       extraArgs,
		costPer1kTokens: costPer1kTokens,
		capabilities:    []Capability{CapabilityFileEdit, CapabilityBash},
	}}
}

func (a *GeminiCLIAdapter) Name() string                                    { return a.shell.adapterName() }
func (a *GeminiCLIAdapter) Capabilities() []Capability                     { return a.shell.adapterCapabilities() }
func (a *GeminiCLIAdapter) CostEstimate(n int) (float64, bool)             { return a.shell.adapterCostEstimate(n) }
func (a *GeminiCLIAdapter) Run(ctx context.Context, rc RunContext) (RunHandle, error) {
	return a.shell.adapterRun(ctx, rc)
}
