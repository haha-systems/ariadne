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

// ResultHandler is the primary extension point for reacting to completed runs.
//
// The gateway invokes every registered ResultHandler (in registration order,
// though order is not guaranteed to be stable across runs) after a run
// reaches a terminal state (succeeded/failed/cancelled). It is called from
// the internal async goroutine that drove execution.
//
// Relationship to policy.Engine.PostRun:
//   - The gateway ALWAYS calls policy.PostRun first (if a non-nil policy was
//     provided at construction; NoopEngine is a safe no-op).
//   - Then it calls all ResultHandlers.
//   - Both mechanisms are additive. PostRun is for *policy* concerns (learning,
//     metrics, global side effects). ResultHandlers are for *result delivery*
//     (proof artifacts, notifications to the originating system, webhooks,
//     updating external trackers).
//   - Future Starlark post_run (Phase 3) will be surfaced via a StarlarkEngine's
//     PostRun; it will not directly replace or bypass ResultHandlers.
//
// Intended usage for adapters (MCP server today, Discord/Signal/cron/CLI tomorrow):
//   - An adapter constructs a Gateway (often via New with a SupervisorExecutor).
//   - It may pass one or more ResultHandlers in Config.ResultHandlers (they
//     become the initial set, plus the automatic LoggingResultHandler unless
//     a noopResultHandler is explicitly provided).
//   - It may call gw.RegisterResultHandler(...) at any time for additional
//     per-adapter handlers (e.g. a DiscordResultHandler that posts a message
//     containing the run summary and proof path back to the thread/channel
//     identified via inv.Metadata["discord_thread_id"]).
//   - The adapter's handler receives a *snapshot* copy of the Run and the
//     original Invocation; it must not mutate them.
//   - The 'outcome' parameter is currently unused (always nil) but reserved
//     for richer executor return values in the future.
//
// How to implement a new handler (easy for future developers):
//  1. Define a struct implementing Handle (must be concurrency-safe; use
//     internal locking if you mutate your own state).
//  2. Provide a constructor (e.g. NewMyHandler(...) *MyHandler).
//  3. Optionally support functional options for configuration (timeouts, etc.).
//  4. In Handle, do your work (write files, HTTP calls, etc.). Return error
//     only for hard failures; the gateway currently ignores handler errors
//     (best-effort semantics).
//  5. Register via Config at New time or gw.RegisterResultHandler later.
//  6. Document any metadata keys your handler expects in the Invocation
//     (e.g. "webhook_url", "callback_id").
//
// Built-in handlers provided by this package (see result_handlers.go):
//   - LoggingResultHandler (always present by default).
//   - ProofSummaryResultHandler (writes a minimal gateway_summary.json into the
//     worktree's proof/ directory — useful baseline for direct/gateway runs;
//     distinct from the richer proof/summary.json written by legacy collector).
//   - WebhookResultHandler (configurable POST of a JSON payload on completion).
//
// Example (in an adapter):
//
//	h := gateway.NewWebhookResultHandler("https://example.com/cb", gateway.WithWebhookTimeout(5*time.Second))
//	gw, _ := gateway.New(cfg, exec)  // cfg may also contain initial handlers
//	gw.RegisterResultHandler(h)
//
// Implementations live in the same package today for simplicity; external
// adapters just satisfy the interface.
type ResultHandler interface {
	Handle(ctx context.Context, run *Run, inv *Invocation, outcome any) error
}

// Gateway is the central control plane. All adapters (MCP, Discord, Signal, cron,
// CLI direct, legacy poller, etc.) go through this interface.
// The gateway owns routing (via policy), execution (via injected Executor),
// cancellation, and post-run result delivery (via policy.PostRun + ResultHandlers).
// It does not know which adapter originated the work — that information lives
// in Invocation.Source / Metadata and is used by handlers and policy.
type Gateway interface {
	// Submit validates the invocation (via policy if configured), routes it,
	// prepares the workspace, launches the agent, and returns immediately.
	Submit(ctx context.Context, inv Invocation) (*Run, error)

	// Cancel requests cancellation for a running invocation.
	// Returns true if a cancel was delivered, false if the run was already terminal.
	Cancel(runID string) (bool, error)

	// GetRun returns the current view of a run (best effort; may be slightly stale).
	GetRun(runID string) (*Run, error)

	// ListRuns returns recent runs (implementation-defined ordering and limits).
	ListRuns(limit int) ([]*Run, error)

	// RegisterResultHandler adds a handler that will be invoked on terminal runs
	// (after policy.PostRun, in addition to any handlers supplied at construction).
	// Order of invocation is the order of registration but is not guaranteed
	// across different runs; handlers must be safe for concurrent use and
	// should be idempotent for their side effects where possible.
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
	// Policy is the policy engine used for routing and hooks.
	// If nil, a no-op engine is used (all decisions fall through to defaults).
	Policy policy.Engine
	// ResultHandlers are optional handlers supplied at construction time.
	// The gateway automatically ensures a LoggingResultHandler is present
	// unless the caller explicitly includes a noopResultHandler (advanced).
	// Additional handlers can be registered later via RegisterResultHandler.
	// See ResultHandler godoc for the full adapter + extensibility model.
	ResultHandlers []ResultHandler
}
