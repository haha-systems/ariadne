package adapters

import (
	"errors"

	"github.com/haha-systems/ariadne/internal/gateway"
)

// Package adapters contains the minimal "multiple adapters, one core" spikes
// for Phase 2 Task E: CronAdapter and CommsStub (plus FakeTransport).
//
// These demonstrate that the MCP server, cron jobs, chat integrations (Discord,
// Signal, ...), and future sources are peers: they all translate their events
// into gateway.Invocation and feed the exact same gateway.Gateway (which owns
// routing policy, execution, and result handlers).
//
// See adapter.go (this file) for the contracts and gateway/types.go for the
// authoritative interface definitions.

// Adapter is the minimal lifecycle interface for any input adapter that
// submits work to the central Ariadne Gateway.
//
// See the full documentation and rationale on the definition in
// gateway.Adapter (the authoritative copy). This is a type alias provided
// here for ergonomic imports when working with the adapter spikes:
//
//     import "github.com/haha-systems/ariadne/internal/adapters"
//     var _ adapters.Adapter = (*adapters.CronAdapter)(nil)
//
// This follows the project convention (interfaces defined in the consuming
// package) while keeping the natural "adapters" home for the concrete spike
// implementations.
type Adapter = gateway.Adapter

// CommsAdapter is the communications adapter interface (Discord/Signal/Telegram
// style bidirectional adapters) as specified by Task E.
//
// Per the task: "Create a `CommsAdapter` skeleton: define an interface that a
// Discord/Signal/Telegram-style adapter would implement, plus a fake or stub
// implementation."
//
// CommsAdapter is satisfied by CommsStub (the provided spike) and any future
// real platform adapter. It is a type alias to the general Adapter because
// the shape (Start/Stop lifecycle + Submit + optional ResultHandler
// registration) is identical for all adapters; the distinction is only
// documentary and for future possible specialization.
type CommsAdapter = Adapter

// ErrAlreadyStopped is wrapped by Start when called on an adapter that has
// already been Stopped.
var ErrAlreadyStopped = errors.New("already stopped")

