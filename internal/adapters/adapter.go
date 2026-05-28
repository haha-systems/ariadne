package adapters

import (
	"context"
)

// Adapter is the minimal lifecycle interface for any input adapter that
// submits work to the central Ariadne Gateway.
//
// Design:
//
//   - Adapters are thin. Their only job is to translate external concepts
//     (chat messages, cron schedules, webhooks, CLI args, ...) into
//     gateway.Invocation values and call gw.Submit (or Cancel, etc.).
//   - All execution policy, routing (SelectRoute), workspace prep, agent
//     invocation, result handling (PostRun hooks, ProofSummary, Webhook,
//     custom reply handlers, ...) and observability live exclusively in the
//     Gateway and its collaborators.
//   - Therefore, adding a new source (Discord bot, Signal bot, a real cron
//     with cronexpr, a GitHub App webhook receiver, ...) is "just" writing
//     a new type that satisfies Adapter and knows how to map its domain
//     into Invocations + (optionally) how to register ResultHandlers that
//     can send replies back.
//
// Start(ctx) should return promptly. Long-running work (tickers, network
// listeners, message pumps) belongs in goroutines launched from Start.
// The ctx passed to Start is the parent lifecycle context; adapters should
// stop when it is cancelled.
//
// Stop must be safe to call multiple times and should wait for in-flight
// submissions to drain where practical.
//
// Registration with the Gateway happens naturally via dependency injection
// at construction time:
//
//     gw, _ := gateway.New(cfg, exec)
//     cron := adapters.NewCronAdapter(gw, 30*time.Second)
//     comms := adapters.NewDiscordStub(gw, transport)
//
//     // For bidirectional adapters, the comms impl typically does:
//     //   gw.RegisterResultHandler(myReplyHandler)  inside Start or New
//     // so that terminal runs can trigger "send message back to channel".
//
// This package (and the two spikes below) are deliberately minimal proofs
// of the "multiple adapters, one core" architecture that Phase 2 set out
// to demonstrate. They are not intended for production use.
//
// Interface location note (project convention):
// Per the rule that "interfaces are defined in the consuming package", a
// production version could define Adapter in cmd/ariadne or a future
// internal/orchestrator package that wires everything. For this focused
// spike we locate the contract here — the adapters package is the natural
// home for the "input adapter" abstraction, exactly like internal/provider
// owns AgentProvider and internal/policy owns Engine. Consumers simply
// import "github.com/haha-systems/ariadne/internal/adapters".
type Adapter interface {
	// Start begins the adapter's work. It must return quickly; background
	// activity runs in goroutines. Implementations should respect ctx
	// cancellation for shutdown.
	Start(ctx context.Context) error

	// Stop requests a clean shutdown. Implementations must be idempotent.
	Stop() error
}
