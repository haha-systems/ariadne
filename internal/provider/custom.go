package provider

import (
	"context"
	"fmt"
	"strings"
)

// CustomAdapter runs any command-line agent specified in ariadne.toml.
// Template variables in extraArgs are substituted before execution:
//
//	{{task_file}} → RunContext.TaskFile
//	{{run_id}}    → RunContext.RepoPath base directory name (approximation)
type CustomAdapter struct {
	shell shellAdapter
}

func NewCustomAdapter(name, binary string, extraArgs []string, costPer1kTokens float64) *CustomAdapter {
	if name == "" {
		name = "custom"
	}
	return &CustomAdapter{shell: shellAdapter{
		name:            name,
		binary:          binary,
		extraArgs:       extraArgs,
		costPer1kTokens: costPer1kTokens,
		capabilities:    []Capability{CapabilityFileEdit, CapabilityBash},
	}}
}

func (a *CustomAdapter) Name() string                        { return a.shell.adapterName() }
func (a *CustomAdapter) Capabilities() []Capability         { return a.shell.adapterCapabilities() }
func (a *CustomAdapter) CostEstimate(n int) (float64, bool) { return a.shell.adapterCostEstimate(n) }

func (a *CustomAdapter) Run(ctx context.Context, rc RunContext) (RunHandle, error) {
	if a.shell.binary == "" {
		return nil, fmt.Errorf("custom adapter %q: binary must be set", a.shell.name)
	}
	// Substitute template variables in extra args. Use a local copy of the
	// shellAdapter so we never mutate the shared struct (race condition).
	resolved := make([]string, len(a.shell.extraArgs))
	for i, arg := range a.shell.extraArgs {
		resolved[i] = applyTemplates(arg, rc)
	}
	local := a.shell
	local.extraArgs = resolved
	return local.adapterRun(ctx, rc)
}

func applyTemplates(s string, rc RunContext) string {
	s = strings.ReplaceAll(s, "{{task_file}}", rc.TaskFile)
	s = strings.ReplaceAll(s, "{{repo_path}}", rc.RepoPath)
	return s
}
