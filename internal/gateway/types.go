package gateway

import (
	"context"
	"time"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/policy"
)

// Invocation is the clean input to the gateway for starting a run.
// It is deliberately slim compared to domain.Task (no GitHub/Linear-specific
// fields like Branch, ReviewCycle, etc.). Adapters (MCP, Discord, cron, etc.)
// map their own concepts into this.
type Invocation struct {
	// ID is optional; if empty the gateway will generate one.
	ID string

	Title       string
	Prompt      string            // The actual task body/prompt for the agent.
	Labels      []string          // Optional, for Starlark policy or future routing.
	Provider    string            // Explicit provider pin (or empty to let policy decide).
	Persona     string            // Explicit persona pin (or empty).
	Routing     string            // Optional routing strategy override.
	Timeout     time.Duration     // 0 means use config default.
	Env         map[string]string // Additional env for this invocation.
	Metadata    map[string]string // Free-form (source adapter, conversation ID, cron job ID, etc.).
	Source      string            // e.g. "mcp", "discord", "cron", "cli" — for policy and observability.
	SourceURL   string            // Optional URL from the originating system (issue, thread, etc.).
	PublishMode string            // Optional override for proof publish behavior (if relevant).
}

// Run is the lightweight handle returned by Submit (and used for Get/Cancel/etc.).
// It is the gateway's view of a run in progress or completed.
type Run struct {
	ID        string
	TaskID    string // Stable identifier for the work item (may be generated).
	Title     string
	Provider  string
	Persona   string
	Status    domain.RunStatus
	Worktree  string // Absolute path to the isolated workspace.
	StartedAt time.Time
	// FinishedAt is nil while running.
	FinishedAt *time.Time
	LastEvent  string
	LastError  string
	// ProofPath, PRURL, etc. are populated by result handlers / post-run hooks
	// (not the gateway core itself).
	ProofPath   string
	Walkthrough string
	Metadata    map[string]string
}

// ResultHandler is called by the gateway (or via Starlark policy) after a run
// reaches a terminal state. This is the extension point for "what happens next"
// (write proof, post to Discord, open PR, call webhook, etc.).
// Implementations must be safe for concurrent calls.
type ResultHandler interface {
	Handle(ctx context.Context, run *Run, inv *Invocation, outcome any) error
}

// Gateway is the central control plane. All adapters (MCP, Discord, Signal, cron,
// CLI direct, legacy poller, etc.) go through this interface.
// The gateway owns routing (via policy.Engine), PreRun/PostRun hooks,
// execution, cancellation, and basic observability. It does not know which
// adapter originated the work.
type Gateway interface {
	// Submit validates the invocation (via policy if configured), routes it
	// (SelectRoute), runs PreRun hooks (which may mutate or veto), prepares
	// the workspace, launches the agent, and returns immediately.
	Submit(ctx context.Context, inv Invocation) (*Run, error)

	// Cancel requests cancellation for a running invocation.
	// Returns true if a cancel was delivered, false if the run was already terminal.
	Cancel(runID string) (bool, error)

	// GetRun returns the current view of a run (best effort; may be slightly stale).
	GetRun(runID string) (*Run, error)

	// ListRuns returns recent runs (implementation-defined ordering and limits).
	ListRuns(limit int) ([]*Run, error)

	// RegisterResultHandler adds a handler that will be invoked on terminal runs.
	// Order is not guaranteed; handlers must be idempotent where it matters.
	RegisterResultHandler(h ResultHandler)

	// Close stops background work (if any) and releases resources.
	Close() error
}

// Config captures the static configuration the gateway needs from ariadne.toml
// (plus any runtime overrides).
type Config struct {
	RepoRoot        string
	Providers       map[string]config.ProviderConfig
	Personas        map[string]config.PersonaConfig
	DefaultProvider string
	Sandbox         config.SandboxConfig
	Skills          map[string]config.SkillConfig
	// Policy is the policy engine used for routing (SelectRoute) and hooks
	// (PreRun + PostRun). If nil, a NoopEngine is used.
	//
	// StarlarkEngine is the recommended implementation for Phase 3+.
	// It gives policies access to a rich mutable invocation dict plus safe
	// builtins (json, restricted read_file, log, memory, list_*).
	Policy policy.Engine
	// ResultHandlers are the built-in ones (more can be registered at runtime).
	ResultHandlers []ResultHandler
}