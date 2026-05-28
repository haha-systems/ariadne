# Adapters — Phase 2 Gateway Spikes (Task E)

This package contains the minimal adapter pattern and two spikes that complete the demonstration of the "multiple adapters, one core" model for Ariadne's refactored gateway.

## The Pattern

All input sources — whether the existing MCP server, a cron scheduler, a Discord bot, a Signal listener, a webhook receiver, or a direct CLI path — are now understood as **adapters**.

An adapter's responsibilities are deliberately tiny:

1. Translate its native events into `gateway.Invocation` values.
2. Call `gw.Submit(inv)` (and optionally `Cancel`, `GetRun`, `ListRuns`).
3. For bidirectional cases (chat platforms), register one or more `ResultHandler`s (via `gw.RegisterResultHandler`) so that terminal runs can trigger replies, notifications, or side effects back to the originating channel/thread.

Everything else — policy (`SelectRoute`, `PostRun`), execution via `Executor`/`SupervisorExecutor`, result delivery, proof collection, run state, etc. — lives exclusively inside the `gateway` package and its collaborators.

This is the concrete payoff of Phase 2 (Tasks A–E):

- Task A made `ariadne mcp` a thin adapter that only creates a `Gateway`.
- Task D strengthened `PostRun` + built-in handlers (`ProofSummary`, `Webhook`).
- Task E (this) proves that cron, comms, etc. are the *same shape*.

Adding a new source never requires forking execution logic or duplicating the policy engine.

## The Adapter Interface

```go
type Adapter interface {
    Start(ctx context.Context) error
    Stop() error
}
```

(See `adapter.go` for the full godoc and rationale on interface placement.)

Construction always takes the `gateway.Gateway` (and adapter-specific deps such as a transport or interval). Start/Stop manage the adapter's own goroutines. No globals, explicit dependencies only.

Registration with the Gateway is implicit: the adapter holds the `gw` reference and may call `RegisterResultHandler` (and `Submit`) on it.

## The Two Spikes

### 1. Cron Spike (`cron.go`)

`CronAdapter` uses a plain `time.Ticker` to submit a fixed-form `Invocation` (Source = `"cron"`, plus correlation metadata) on a schedule.

```go
gw, _ := gateway.New(...)
cron := adapters.NewCronAdapter(gw, 5*time.Second)
cron.Start(ctx)
...
cron.Stop()
```

No job storage, no cron expression parser, no persistence. Purely architectural.

### 2. Comms Stub + FakeTransport (`comms.go`)

`CommsStub` + `FakeTransport` is a fully runnable, channel-driven simulation of a Discord/Signal-style adapter.

- `InjectMessage(...)` simulates inbound platform events.
- The stub maps them to Invocations carrying `comms_*` metadata for correlation.
- It registers a `ResultHandler` (using the tiny local `ResultHandlerFunc` helper) that turns completed runs back into `FakeReply` values on the transport.
- Tests can drive the whole round-trip without any network or real SDK.

This is the strongest proof that the reply path (one of the key deliverables of Task D) works for *any* adapter that chooses to register a handler.

A real implementation would look almost identical, just swapping channel operations for:

- Discord session event handlers + `session.ChannelMessageSend(...)`
- Signal gRPC or libsignal client sends
- etc.

## Wiring Alongside MCP (and Future Adapters)

In `cmd/ariadne/mcp.go` (or any future main), the pattern is:

```go
gw, _ := gateway.New(gateway.Config{...}, exec)

mcpServer := mcpserver.New(..., mcpserver.Config{Gateway: gw})

cron := adapters.NewCronAdapter(gw, 30*time.Second)
comms := adapters.NewCommsStub(gw, adapters.NewFakeTransport())

ctx, cancel := context.WithCancel(...)
defer cancel()

_ = cron.Start(ctx)
_ = comms.Start(ctx)
_ = mcpServer.ListenAndServe(ctx)  // blocks until ctx done

cron.Stop()
comms.Stop()
```

All three adapters feed the exact same `Gateway`. Adding or removing one has zero impact on the others.

## Tests

See `*_test.go` files in this package. They use minimal `fakeGateway` doubles (no supervisor, no providers) so the adapter logic itself can be exercised quickly and deterministically, including concurrent ticker + result handler scenarios (run with `-race`).

## Non-Goals (by design)

- No production scheduler (no `cronexpr`, no persistent jobs, no jitter/backoff).
- No real Discord/Signal/Telegram client code or auth.
- No changes to `gateway.Gateway` itself for this spike (the existing `RegisterResultHandler` + `Submit` surface is sufficient).
- No modification of the legacy `worksource` / `operator` paths.
- The spikes are intentionally small so the architectural signal is obvious.

## Follow-up Ideas (for later phases)

- Promote useful helpers (e.g. a more complete `ResultHandlerFunc`, correlation utilities) into `gateway` or a shared `internal/adapterutil`.
- Make `Gateway` optionally own a set of `Adapter`s with `RegisterAdapter` + shutdown coordination.
- Extract the MCP server bits that only do "map to Invocation + Submit" into a reusable `MCPAdapter` type that also satisfies `adapters.Adapter`.
- Real cron adapter with pluggable job specs + memory-backed last-fire tracking.
- Production comms adapters (one per platform) behind a common message model.

This package (the interface + two spikes + this note) is the final concrete artifact of Phase 2.

See also:
- `internal/gateway/types.go` (Invocation, Gateway, ResultHandler)
- `internal/gateway/gateway.go` (the implementation)
- `internal/mcpserver/server.go` (the current real adapter using the gateway path)
- Task D result handler work (ProofSummary, Webhook, PostRun wiring)
