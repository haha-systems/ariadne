package adapters

import (
	"context"
	"fmt"
	"sync"

	"github.com/haha-systems/ariadne/internal/gateway"
)

// FakeTransport is an in-memory stand-in for a real messaging platform
// (Discord, Signal, Telegram, Slack, ...).
//
// Tests and the spike demo drive it by calling InjectMessage (simulating an
// incoming user message or mention) and reading from SentReplies (what the
// adapter "sent back" via its registered ResultHandler).
//
// This is the key demonstration artifact for bidirectional adapters:
//   - Receive path: transport -> map to Invocation -> gw.Submit
//   - Reply path:   terminal run -> ResultHandler (registered by the stub)
//                   -> transport.SendReply (the "post message" side effect)
//
// A real adapter would replace the channels with actual client SDK calls
// (websocket event loop for Discord, etc.) while keeping the exact same
// mapping + result handler registration logic.
type FakeTransport struct {
	incoming chan FakeMessage // adapter reads "received" messages from here
	sent     chan FakeReply   // adapter writes replies here (test drains it)

	mu     sync.Mutex
	closed bool
}

// FakeMessage represents an inbound message from the simulated platform.
type FakeMessage struct {
	ID        string // platform message / snowflake ID
	ChannelID string // thread / channel / DM identifier
	AuthorID  string
	Content   string
}

// FakeReply is what the adapter "sends" back (via its ResultHandler).
type FakeReply struct {
	ChannelID string
	Content   string // e.g. a proof summary or "run completed" notice
}

// NewFakeTransport creates a transport with modest buffering so tests
// don't block on slow handler execution.
func NewFakeTransport() *FakeTransport {
	return &FakeTransport{
		incoming: make(chan FakeMessage, 16),
		sent:     make(chan FakeReply, 16),
	}
}

// InjectMessage is the test hook to simulate a user sending a message to
// the bot (or mentioning it in a thread). The stub's receive pump will
// pick it up and turn it into a gateway.Invocation.
func (t *FakeTransport) InjectMessage(m FakeMessage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	select {
	case t.incoming <- m:
	default:
		// drop if full — fine for spike
	}
}

// SentReplies returns the channel that receives FakeReply values when the
// stub's result handler decides to reply. Tests range over or receive from it.
func (t *FakeTransport) SentReplies() <-chan FakeReply {
	return t.sent
}

// Close shuts down the transport channels (idempotent).
func (t *FakeTransport) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	close(t.incoming)
	close(t.sent)
}

// CommsStub is the skeleton implementation of a Discord/Signal-style
// bidirectional communications adapter (satisfies CommsAdapter).
//
// It is a complete, runnable, self-contained demonstration (with the
// FakeTransport) showing the full round-trip:
//
//  1. External event arrives (InjectMessage).
//  2. Stub maps it to a gateway.Invocation carrying correlation metadata
//     (channel ID, original message ID, source="comms").
//  3. Stub calls gw.Submit — the *only* entry point into the core.
//  4. On run completion the Gateway invokes all registered ResultHandlers.
//  5. The handler registered by CommsStub reads the correlation metadata
//     from the original Invocation and "replies" via the transport.
//
// Exactly the same pattern a real Discord adapter would use (substituting
// the transport for a real session + channel send).
type CommsStub struct {
	gw        gateway.Gateway
	transport *FakeTransport

	stop   chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
	stopped bool

	// handlerRegistered is used to avoid double-registering the reply handler.
	handlerRegistered bool
}

// NewCommsStub constructs a CommsStub for the given gateway and transport.
// The stub will register its own reply-handling ResultHandler on first Start.
func NewCommsStub(gw gateway.Gateway, transport *FakeTransport) *CommsStub {
	if transport == nil {
		transport = NewFakeTransport()
	}
	return &CommsStub{
		gw:        gw,
		transport: transport,
		stop:      make(chan struct{}),
	}
}

// Start implements gateway.Adapter / CommsAdapter. It registers the reply ResultHandler (once) and
// launches the inbound message pump.
func (c *CommsStub) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return fmt.Errorf("comms stub: %w", ErrAlreadyStopped)
	}
	if !c.handlerRegistered {
		c.gw.RegisterResultHandler(c.replyHandler())
		c.handlerRegistered = true
	}
	c.mu.Unlock()

	c.wg.Add(1)
	go c.pump(ctx)
	return nil
}

// replyHandler returns the ResultHandler that turns terminal runs originating
// from this comms adapter into platform replies.
//
// It inspects the Invocation's Source and Metadata to decide whether (and
// where) to reply. In a real adapter the logic would be richer (e.g. only
// reply on success, include proof URL, respect user mention style, etc.).
func (c *CommsStub) replyHandler() gateway.ResultHandler {
	return resultHandlerFunc(func(ctx context.Context, run *gateway.Run, inv *gateway.Invocation, outcome any) error {
		if inv == nil || inv.Source != "comms" {
			return nil // not ours
		}
		channelID := inv.Metadata["comms_channel_id"]
		if channelID == "" {
			return nil
		}

		// Build a minimal human-friendly reply body.
		// Real adapters would pull more from the proof collector or outcome.
		content := fmt.Sprintf("Run %s (%s) finished: %s (worktree: %s)",
			run.ID, run.Title, run.Status, run.Worktree)

		reply := FakeReply{
			ChannelID: channelID,
			Content:   content,
		}

		// Non-blocking send into the fake transport.
		select {
		case c.transport.sent <- reply:
		default:
		}
		return nil
	})
}

// pump is the receive loop. It turns inbound FakeMessages into Invocations
// and submits them. It exits on parent ctx or explicit Stop.
func (c *CommsStub) pump(parentCtx context.Context) {
	defer c.wg.Done()

	for {
		select {
		case <-parentCtx.Done():
			return
		case <-c.stop:
			return
		case msg, ok := <-c.transport.incoming:
			if !ok {
				return
			}
			inv := gateway.Invocation{
				Title:  fmt.Sprintf("comms: %s", msg.Content),
				Prompt: msg.Content,
				Source: "comms",
				Metadata: map[string]string{
					"adapter":           "comms-stub",
					"comms_message_id":  msg.ID,
					"comms_channel_id":  msg.ChannelID,
					"comms_author_id":   msg.AuthorID,
					"original_content":  msg.Content,
				},
			}
			// Submit; real adapter might also ack the platform message here.
			_, _ = c.gw.Submit(context.Background(), inv)
		}
	}
}

// Stop implements Adapter (idempotent).
func (c *CommsStub) Stop() error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil
	}
	c.stopped = true
	close(c.stop)
	c.mu.Unlock()

	c.wg.Wait()
	return nil
}

// InjectMessage is a convenience for tests (delegates to the transport).
func (c *CommsStub) InjectMessage(m FakeMessage) {
	c.transport.InjectMessage(m)
}

// SentReplies returns the receive side of the reply channel.
func (c *CommsStub) SentReplies() <-chan FakeReply {
	return c.transport.SentReplies()
}

// Ensure CommsStub satisfies Adapter and CommsAdapter (compile-time checks).
var _ Adapter = (*CommsStub)(nil)
var _ CommsAdapter = (*CommsStub)(nil)

// resultHandlerFunc is an unexported tiny adapter type (spike-internal only)
// so we can use a plain func as a ResultHandler inside the comms stub demo.
// It satisfies gateway.ResultHandler. (Unexported per spec compliance review.)
type resultHandlerFunc func(ctx context.Context, run *gateway.Run, inv *gateway.Invocation, outcome any) error

func (f resultHandlerFunc) Handle(ctx context.Context, run *gateway.Run, inv *gateway.Invocation, outcome any) error {
	if f == nil {
		return nil
	}
	return f(ctx, run, inv, outcome)
}
