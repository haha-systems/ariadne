package provider

import (
	"context"
	"io"
)

// Capability describes a feature an agent provider supports.
type Capability string

const (
	CapabilityFileEdit Capability = "file_edit"
	CapabilityBash     Capability = "bash"
	CapabilityBrowser  Capability = "browser"
)

// RunContext is passed to a provider when launching a run.
type RunContext struct {
	// RepoPath is the absolute path to the isolated git worktree.
	RepoPath string
	// TaskFile is the path to the file containing the task prompt.
	// Set as $ARIADNE_TASK_FILE in the subprocess environment.
	TaskFile string
	// Env holds additional environment variables for the subprocess.
	// These are merged on top of the current process environment.
	Env map[string]string
	// LogWriter receives all stdout/stderr from the provider subprocess.
	LogWriter io.Writer
	// TimeoutSeconds is the wall-clock timeout for the run.
	TimeoutSeconds int
	// MCPConfigPath is the path to a temporary MCP config JSON file pointing to
	// the github-mcp-server process. Empty string means no MCP server was started.
	MCPConfigPath string
	// OutputFile is the path where the agent should write a structured
	// invocation.Envelope JSON result. Set as $ARIADNE_OUTPUT_FILE in the
	// subprocess environment. Empty string means no output file is expected.
	OutputFile string
	// ModelOverride requests a specific model for this invocation, as set by
	// an agentprofile.Profile. Only honored by adapters that implement
	// ModelOverrideSupporter and report true; others must ignore it rather
	// than passing it through as an unrecognized flag.
	ModelOverride string
}

// ModelOverrideSupporter is implemented by adapters that can honor
// RunContext.ModelOverride. Adapters that don't implement this interface are
// assumed not to support per-invocation model selection.
type ModelOverrideSupporter interface {
	SupportsModelOverride() bool
}

// RunHandle represents a running provider subprocess.
type RunHandle interface {
	// Wait blocks until the provider process exits and returns any error.
	Wait() error
	// Cancel terminates the provider process (SIGTERM then SIGKILL).
	Cancel() error
}

// AgentProvider is the interface all provider adapters must implement.
type AgentProvider interface {
	// Name returns the provider's unique identifier (matches ariadne.toml key).
	Name() string
	// Capabilities returns the set of capabilities this provider supports.
	Capabilities() []Capability
	// CostEstimate returns an estimated USD cost for the given task prompt length.
	// Returns (0, false) if the provider cannot estimate cost.
	CostEstimate(promptLen int) (float64, bool)
	// Run launches the agent subprocess and returns a handle to it.
	Run(ctx context.Context, rc RunContext) (RunHandle, error)
}
