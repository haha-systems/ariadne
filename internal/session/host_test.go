package session_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/haha-systems/ariadne/internal/control"
	"github.com/haha-systems/ariadne/internal/session"
)

// stubController is the minimum a session must supply. The optional
// surfaces return ErrNotSupported, which is what a workflow with no memory
// or escalations legitimately does.
type stubController struct {
	desc session.Descriptor
	caps session.Capabilities
}

func (s *stubController) Describe(context.Context) (session.Descriptor, error) {
	return s.desc, nil
}
func (s *stubController) Capabilities() session.Capabilities { return s.caps }
func (s *stubController) Snapshot(context.Context) (control.ResearchSnapshot, error) {
	return control.ResearchSnapshot{}, nil
}
func (s *stubController) Subscribe(context.Context) (<-chan control.Event, error) {
	return nil, session.Unsupported("events")
}
func (s *stubController) SubscribeLogs(context.Context) (<-chan control.AgentLogEntry, error) {
	return nil, session.Unsupported("logs")
}
func (s *stubController) RecentEvents(context.Context, int) ([]control.Event, error) {
	return nil, nil
}
func (s *stubController) AgentLog(context.Context, string, int) ([]control.AgentLogEntry, error) {
	return nil, nil
}
func (s *stubController) Escalations(context.Context, int) ([]control.EscalationSummary, error) {
	return nil, session.Unsupported("escalations")
}
func (s *stubController) Memory(context.Context) (control.MemoryView, error) {
	return control.MemoryView{}, session.Unsupported("memory")
}
func (s *stubController) Steering(context.Context) ([]control.SteeringRecord, error) {
	return nil, session.Unsupported("steering")
}
func (s *stubController) Send(context.Context, control.ResearchCommand) error {
	return session.Unsupported("commands")
}

func stub(id string, kind session.Kind, state control.RunState) (session.Descriptor, *stubController) {
	desc := session.Descriptor{ID: id, Kind: kind, Title: id, State: state}
	return desc, &stubController{desc: desc}
}

func TestRegisterAndOpen(t *testing.T) {
	r := session.NewRegistry()
	ctx := context.Background()

	desc, ctrl := stub("research:arachne", session.KindResearch, control.RunStateRunning)
	if err := r.Register(desc, ctrl); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := r.Open(ctx, "research:arachne")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != ctrl {
		t.Fatalf("Open returned a different controller")
	}

	if _, err := r.Open(ctx, "nope"); !errors.Is(err, session.ErrNoSuchSession) {
		t.Fatalf("Open(unknown) = %v, want ErrNoSuchSession", err)
	}
}

func TestRegisterRejectsDuplicateAndEmptyID(t *testing.T) {
	r := session.NewRegistry()

	desc, ctrl := stub("dup", session.KindIssue, control.RunStateRunning)
	if err := r.Register(desc, ctrl); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(desc, ctrl); !errors.Is(err, session.ErrDuplicateSession) {
		t.Fatalf("duplicate Register = %v, want ErrDuplicateSession", err)
	}

	empty, emptyCtrl := stub("", session.KindIssue, control.RunStateRunning)
	if err := r.Register(empty, emptyCtrl); err == nil {
		t.Fatal("Register with empty ID succeeded, want error")
	}
	if err := r.Register(session.Descriptor{ID: "x"}, nil); err == nil {
		t.Fatal("Register with nil controller succeeded, want error")
	}
}

// A session list is a switcher: live work must sort above finished work, or
// the user has to hunt for the thing they are actually doing.
func TestListSortsActiveFirst(t *testing.T) {
	r := session.NewRegistry()
	ctx := context.Background()

	for _, tc := range []struct {
		id    string
		state control.RunState
	}{
		{"done", control.RunStateStopped},
		{"live", control.RunStateRunning},
		{"failed", control.RunStateFailed},
		{"paused", control.RunStatePaused},
	} {
		desc, ctrl := stub(tc.id, session.KindIssue, tc.state)
		if err := r.Register(desc, ctrl); err != nil {
			t.Fatalf("Register(%s): %v", tc.id, err)
		}
	}

	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("List returned %d sessions, want 4", len(list))
	}
	for i, d := range list[:2] {
		if !d.Active() {
			t.Fatalf("list[%d] = %s (%s), want an active session first", i, d.ID, d.State)
		}
	}
	for i, d := range list[2:] {
		if d.Active() {
			t.Fatalf("list[%d] = %s (%s), want inactive sessions last", i+2, d.ID, d.State)
		}
	}
}

func TestUpdatePreservesStartedAtAndNotifies(t *testing.T) {
	r := session.NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watch, err := r.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	desc, ctrl := stub("s1", session.KindResearch, control.RunStateRunning)
	started := time.Now().Add(-time.Hour)
	desc.StartedAt = started
	if err := r.Register(desc, ctrl); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if got := recv(t, watch); got.Kind != session.ChangeAdded || got.Descriptor.ID != "s1" {
		t.Fatalf("first change = %+v, want added s1", got)
	}

	// An update that omits StartedAt must not reset the session's age.
	updated := session.Descriptor{ID: "s1", Kind: session.KindResearch, State: control.RunStatePaused}
	if err := r.Update(updated); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got := recv(t, watch)
	if got.Kind != session.ChangeUpdated {
		t.Fatalf("change kind = %s, want updated", got.Kind)
	}
	if !got.Descriptor.StartedAt.Equal(started) {
		t.Fatalf("StartedAt = %v, want preserved %v", got.Descriptor.StartedAt, started)
	}
	if got.Descriptor.State != control.RunStatePaused {
		t.Fatalf("State = %s, want paused", got.Descriptor.State)
	}

	if err := r.Update(session.Descriptor{ID: "ghost"}); !errors.Is(err, session.ErrNoSuchSession) {
		t.Fatalf("Update(unknown) = %v, want ErrNoSuchSession", err)
	}
}

func TestUnregisterNotifiesAndIsIdempotent(t *testing.T) {
	r := session.NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watch, err := r.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	desc, ctrl := stub("s1", session.KindIssue, control.RunStateRunning)
	if err := r.Register(desc, ctrl); err != nil {
		t.Fatalf("Register: %v", err)
	}
	recv(t, watch) // added

	r.Unregister("s1")
	if got := recv(t, watch); got.Kind != session.ChangeRemoved {
		t.Fatalf("change kind = %s, want removed", got.Kind)
	}

	// A workflow cleaning up after a failure should not need to know
	// whether it got as far as registering.
	r.Unregister("s1")
	r.Unregister("never-existed")

	if _, err := r.Open(ctx, "s1"); !errors.Is(err, session.ErrNoSuchSession) {
		t.Fatalf("Open after Unregister = %v, want ErrNoSuchSession", err)
	}
}

// The whole point of the non-blocking contract: a UI that stops reading
// must never be able to stall the workflow that is producing changes.
func TestSlowWatcherNeverBlocksRegistration(t *testing.T) {
	r := session.NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := r.Watch(ctx); err != nil { // deliberately never read
		t.Fatalf("Watch: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			desc, ctrl := stub(string(rune('a'+i%26))+"-"+time.Now().Format("150405.000000000"),
				session.KindIssue, control.RunStateRunning)
			_ = r.Register(desc, ctrl)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("registration blocked on a watcher that stopped reading")
	}
}

func TestWatchClosesOnContextCancel(t *testing.T) {
	r := session.NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())

	watch, err := r.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	cancel()

	select {
	case _, open := <-watch:
		if open {
			t.Fatal("received a change after cancel, want a closed channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch channel was not closed after context cancel")
	}
}

func TestClosedRegistryRejectsEverything(t *testing.T) {
	r := session.NewRegistry()
	ctx := context.Background()

	desc, ctrl := stub("s1", session.KindResearch, control.RunStateRunning)
	if err := r.Register(desc, ctrl); err != nil {
		t.Fatalf("Register: %v", err)
	}

	r.Close()
	r.Close() // must be idempotent

	if _, err := r.List(ctx); !errors.Is(err, session.ErrClosed) {
		t.Fatalf("List after Close = %v, want ErrClosed", err)
	}
	if _, err := r.Open(ctx, "s1"); !errors.Is(err, session.ErrClosed) {
		t.Fatalf("Open after Close = %v, want ErrClosed", err)
	}
	if _, err := r.Watch(ctx); !errors.Is(err, session.ErrClosed) {
		t.Fatalf("Watch after Close = %v, want ErrClosed", err)
	}
	if err := r.Register(desc, ctrl); !errors.Is(err, session.ErrClosed) {
		t.Fatalf("Register after Close = %v, want ErrClosed", err)
	}
}

func TestConcurrentRegistrationAndListing(t *testing.T) {
	r := session.NewRegistry()
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			desc, ctrl := stub(string(rune('A'+i)), session.KindIssue, control.RunStateRunning)
			_ = r.Register(desc, ctrl)
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.List(ctx)
		}()
	}
	wg.Wait()

	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 32 {
		t.Fatalf("List returned %d sessions, want 32", len(list))
	}
}

// Registry must satisfy the Host contract the harness codes against.
var _ session.Host = (*session.Registry)(nil)

func recv(t *testing.T, ch <-chan session.Change) session.Change {
	t.Helper()
	select {
	case c, open := <-ch:
		if !open {
			t.Fatal("watch channel closed unexpectedly")
		}
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a change")
		return session.Change{}
	}
}
