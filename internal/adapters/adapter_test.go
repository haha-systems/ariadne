package adapters

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/gateway"
)

// fakeGateway is a minimal test double for gateway.Gateway used by the
// adapter tests. It records submitted Invocations and supports registering
// ResultHandlers so that the comms round-trip (reply handler) can be
// exercised without pulling in the full supervisor/provider stack.
//
// It deliberately does not attempt to model real run lifecycle or
// concurrency of the real gateway except where needed for these spikes.
type fakeGateway struct {
	mu       sync.Mutex
	submits  []gateway.Invocation
	handlers []gateway.ResultHandler
	runs     map[string]*gateway.Run // minimal storage for GetRun/List
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{
		runs: make(map[string]*gateway.Run),
	}
}

func (f *fakeGateway) Submit(ctx context.Context, inv gateway.Invocation) (*gateway.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if inv.ID == "" {
		inv.ID = "fake_" + time.Now().Format("20060102150405.000000000")
	}
	f.submits = append(f.submits, inv)

	run := &gateway.Run{
		ID:        inv.ID,
		Title:     inv.Title,
		Provider:  inv.Provider,
		Status:    domain.RunStatusSucceeded, // pretend it finished instantly for handler tests
		StartedAt: time.Now().UTC(),
		Metadata:  copyMapForFake(inv.Metadata),
	}
	f.runs[run.ID] = run

	// For the spike tests we synchronously invoke handlers so the comms
	// reply path is deterministic and doesn't require sleeps.
	snapshot := &gateway.Run{
		ID:        run.ID,
		Title:     run.Title,
		Provider:  run.Provider,
		Status:    run.Status,
		StartedAt: run.StartedAt,
		Metadata:  copyMapForFake(run.Metadata),
	}
	invCopy := gateway.Invocation{
		ID:       inv.ID,
		Title:    inv.Title,
		Prompt:   inv.Prompt,
		Source:   inv.Source,
		Metadata: copyMapForFake(inv.Metadata),
	}
	for _, h := range f.handlers {
		_ = h.Handle(context.Background(), snapshot, &invCopy, nil)
	}

	return run, nil
}

func (f *fakeGateway) Cancel(runID string) (bool, error) { return false, nil }
func (f *fakeGateway) GetRun(runID string) (*gateway.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.runs[runID]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, nil
}
func (f *fakeGateway) ListRuns(limit int) ([]*gateway.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*gateway.Run, 0, len(f.runs))
	for _, r := range f.runs {
		cp := *r
		out = append(out, &cp)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f *fakeGateway) RegisterResultHandler(h gateway.ResultHandler) {
	if h == nil {
		return
	}
	f.mu.Lock()
	f.handlers = append(f.handlers, h)
	f.mu.Unlock()
}
func (f *fakeGateway) Close() error { return nil }

// submittedInvocations returns a copy of all Invocations passed to Submit.
// Safe for concurrent use from tests.
func (f *fakeGateway) submittedInvocations() []gateway.Invocation {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]gateway.Invocation, len(f.submits))
	copy(cp, f.submits)
	return cp
}

// copy helper (avoid import cycle / sharing the unexported one)
func copyMapForFake(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// ---- Tests ----

func TestAdapterInterfaceSatisfied(t *testing.T) {
	// Compile-time guarantees live in the source files.
	// This test just exercises construction + basic Start/Stop.
	gw := newFakeGateway()

	cron := NewCronAdapter(gw, 100*time.Millisecond)
	if cron == nil {
		t.Fatal("NewCronAdapter returned nil")
	}

	comms := NewCommsStub(gw, NewFakeTransport())
	if comms == nil {
		t.Fatal("NewCommsStub returned nil")
	}

	// Both must satisfy Adapter (already asserted by var _ in sources;
	// we can still call the methods).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := cron.Start(ctx); err != nil {
		t.Fatalf("cron.Start: %v", err)
	}
	if err := comms.Start(ctx); err != nil {
		t.Fatalf("comms.Start: %v", err)
	}

	// Stop should be idempotent and safe.
	if err := cron.Stop(); err != nil {
		t.Errorf("cron.Stop first: %v", err)
	}
	if err := cron.Stop(); err != nil {
		t.Errorf("cron.Stop second: %v", err)
	}
	if err := comms.Stop(); err != nil {
		t.Errorf("comms.Stop: %v", err)
	}
}

func TestCronAdapter_SubmitsInvocations(t *testing.T) {
	gw := newFakeGateway()
	// Use a short interval so we can observe submissions without long waits.
	interval := 20 * time.Millisecond
	cron := NewCronAdapter(gw, interval)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	if err := cron.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cron.Stop()

	// Give it time to tick a few times (roughly 150ms / 20ms ≈ 7 ticks).
	time.Sleep(120 * time.Millisecond)

	submits := gw.submittedInvocations()
	if len(submits) < 2 {
		t.Fatalf("expected at least 2 submissions in the window, got %d", len(submits))
	}

	for i, inv := range submits {
		if inv.Source != "cron" {
			t.Errorf("submit[%d]: expected Source='cron', got %q", i, inv.Source)
		}
		if inv.Metadata["adapter"] != "cron" {
			t.Errorf("submit[%d]: missing or wrong adapter metadata", i)
		}
		if inv.Title == "" || inv.Prompt == "" {
			t.Errorf("submit[%d]: title or prompt empty", i)
		}
	}
}

func TestCronAdapter_StopStopsSubmissions(t *testing.T) {
	gw := newFakeGateway()
	cron := NewCronAdapter(gw, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = cron.Start(ctx)
	time.Sleep(30 * time.Millisecond)
	_ = cron.Stop()

	countAfterStop := len(gw.submittedInvocations())

	// Wait a bit longer; no more ticks should arrive.
	time.Sleep(40 * time.Millisecond)

	if len(gw.submittedInvocations()) != countAfterStop {
		t.Errorf("submissions continued after Stop: before=%d after=%d", countAfterStop, len(gw.submittedInvocations()))
	}
}

func TestCommsStub_RoundTripWithResultHandler(t *testing.T) {
	gw := newFakeGateway()
	transport := NewFakeTransport()
	stub := NewCommsStub(gw, transport)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := stub.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer stub.Stop()

	// Simulate an incoming message from the platform.
	msg := FakeMessage{
		ID:        "msg-123",
		ChannelID: "channel-abc",
		AuthorID:  "user-xyz",
		Content:   "please refactor the login module",
	}
	stub.InjectMessage(msg)

	// The pump should have turned it into a Submit almost immediately.
	// Because our fakeGateway calls handlers synchronously in Submit,
	// the reply should already be on the sent channel.
	select {
	case reply := <-transport.SentReplies():
		if reply.ChannelID != "channel-abc" {
			t.Errorf("reply went to wrong channel: %s", reply.ChannelID)
		}
		if reply.Content == "" {
			t.Error("reply content was empty")
		}
		// Sanity: the run ID and status should appear in the crafted reply.
		if !contains(reply.Content, "Run ") || !contains(reply.Content, "finished") {
			t.Errorf("unexpected reply format: %q", reply.Content)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for reply via ResultHandler")
	}

	// Verify the invocation that was submitted carried the correlation data.
	submits := gw.submittedInvocations()
	if len(submits) != 1 {
		t.Fatalf("expected exactly 1 submit, got %d", len(submits))
	}
	inv := submits[0]
	if inv.Source != "comms" {
		t.Errorf("expected Source='comms', got %q", inv.Source)
	}
	if inv.Metadata["comms_channel_id"] != "channel-abc" {
		t.Errorf("correlation metadata missing/wrong: %+v", inv.Metadata)
	}
	if inv.Metadata["comms_message_id"] != "msg-123" {
		t.Errorf("message id not preserved in metadata")
	}
}

func TestCommsStub_MultipleMessagesAndReplies(t *testing.T) {
	gw := newFakeGateway()
	stub := NewCommsStub(gw, nil) // will create its own transport

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = stub.Start(ctx)
	defer stub.Stop()

	// Fire two messages.
	stub.InjectMessage(FakeMessage{ID: "m1", ChannelID: "c1", Content: "task one"})
	stub.InjectMessage(FakeMessage{ID: "m2", ChannelID: "c2", Content: "task two"})

	replies := make([]FakeReply, 0, 2)
	timeout := time.After(500 * time.Millisecond)
	for len(replies) < 2 {
		select {
		case r := <-stub.SentReplies():
			replies = append(replies, r)
		case <-timeout:
			t.Fatalf("only got %d replies, wanted 2", len(replies))
		}
	}

	if len(gw.submittedInvocations()) != 2 {
		t.Errorf("expected 2 submits, got %d", len(gw.submittedInvocations()))
	}
}

// ---- tiny helpers for the test file ----

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
