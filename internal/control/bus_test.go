package control

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestBus_SubscriberReceivesPublishedEvents(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.Subscribe(ctx)

	bus.Log("research_cycle_started", map[string]any{"cycle": 3})

	select {
	case ev := <-ch:
		if ev.Name != "research_cycle_started" {
			t.Errorf("Name = %q", ev.Name)
		}
		if n, ok := ev.Number("cycle"); !ok || n != 3 {
			t.Errorf("cycle = %v, %v", n, ok)
		}
		if ev.Time.IsZero() {
			t.Error("expected the bus to stamp a time")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the event")
	}
}

func TestBus_MultipleSubscribersEachReceiveEveryEvent(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := bus.Subscribe(ctx), bus.Subscribe(ctx)

	bus.Log("e", nil)
	for i, ch := range []<-chan Event{a, b} {
		select {
		case ev := <-ch:
			if ev.Name != "e" {
				t.Errorf("subscriber %d got %q", i, ev.Name)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d timed out", i)
		}
	}
}

// A slow subscriber must never apply backpressure to research: the whole
// point of the bus is that publishing is a side effect of doing the work,
// not a step in it.
func TestBus_SlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.Subscribe(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more than the subscriber's buffer, with nobody reading.
		for i := 0; i < defaultBufferSize*4; i++ {
			bus.Log("flood", map[string]any{"i": i})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing blocked on a subscriber that stopped reading")
	}

	if bus.Dropped() == 0 {
		t.Error("expected the bus to report dropped events for the slow subscriber")
	}
	if len(ch) == 0 {
		t.Error("expected the subscriber to still hold a buffered backlog")
	}
}

func TestBus_CancellingContextClosesChannelAndUnsubscribes(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch := bus.Subscribe(ctx)
	if bus.Subscribers() != 1 {
		t.Fatalf("Subscribers() = %d, want 1", bus.Subscribers())
	}

	cancel()
	select {
	case _, open := <-ch:
		if open {
			// Drain any buffered event, then the close must follow.
			select {
			case _, open := <-ch:
				if open {
					t.Error("expected the channel to close after cancellation")
				}
			case <-time.After(time.Second):
				t.Error("timed out waiting for close")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the channel to close")
	}

	deadline := time.Now().Add(time.Second)
	for bus.Subscribers() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if bus.Subscribers() != 0 {
		t.Errorf("Subscribers() = %d after cancellation, want 0", bus.Subscribers())
	}
}

func TestBus_CloseClosesEverySubscriber(t *testing.T) {
	bus := NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.Subscribe(ctx)

	bus.Close()
	select {
	case _, open := <-ch:
		if open {
			t.Error("expected a closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	// Publishing after close must be a no-op, not a panic on a closed
	// channel — shutdown races are normal, not exceptional.
	bus.Log("after-close", nil)
}

func TestLogBus_HistoryIsPerAgentAndBounded(t *testing.T) {
	bus := NewLogBus(3)
	for i := 0; i < 5; i++ {
		bus.Publish(AgentLogEntry{Agent: "ari", Text: fmt.Sprintf("ari-%d", i)})
	}
	bus.Publish(AgentLogEntry{Agent: "astra", Text: "astra-0"})

	ari := bus.History("ari", 0)
	if len(ari) != 3 {
		t.Fatalf("len(ari history) = %d, want the ring bounded to 3", len(ari))
	}
	if ari[0].Text != "ari-2" || ari[2].Text != "ari-4" {
		t.Errorf("expected the newest 3 entries, got %v", ari)
	}

	// Ari's and Astra's streams stay separable — merging them is exactly
	// what makes a live log unreadable.
	astra := bus.History("astra", 0)
	if len(astra) != 1 || astra[0].Text != "astra-0" {
		t.Errorf("astra history = %v", astra)
	}

	all := bus.History("", 0)
	if len(all) != 6 {
		t.Errorf("len(all history) = %d, want every entry in arrival order", len(all))
	}
}

func TestLogBus_SubscriberReceivesEntriesAndSlowOneDoesNotBlock(t *testing.T) {
	bus := NewLogBus(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.Subscribe(ctx)

	bus.Publish(AgentLogEntry{Agent: "ari", Text: "hello"})
	select {
	case entry := <-ch:
		if entry.Text != "hello" {
			t.Errorf("Text = %q", entry.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < defaultBufferSize*3; i++ {
			bus.Publish(AgentLogEntry{Agent: "ari", Text: "flood"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("log publishing blocked on a slow subscriber")
	}
}
