package harness_test

import (
	"context"
	"testing"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/control"
	"github.com/haha-systems/ariadne/internal/harness"
	"github.com/haha-systems/ariadne/internal/session"
)

// stubSession is the minimum a session must be to appear in the harness.
type stubSession struct{ desc session.Descriptor }

func (s *stubSession) Describe(context.Context) (session.Descriptor, error) { return s.desc, nil }
func (s *stubSession) Capabilities() session.Capabilities                   { return nil }
func (s *stubSession) Snapshot(context.Context) (control.ResearchSnapshot, error) {
	return control.ResearchSnapshot{}, nil
}
func (s *stubSession) Subscribe(context.Context) (<-chan control.Event, error) { return nil, nil }
func (s *stubSession) SubscribeLogs(context.Context) (<-chan control.AgentLogEntry, error) {
	return nil, nil
}
func (s *stubSession) RecentEvents(context.Context, int) ([]control.Event, error) { return nil, nil }
func (s *stubSession) AgentLog(context.Context, string, int) ([]control.AgentLogEntry, error) {
	return nil, nil
}
func (s *stubSession) Escalations(context.Context, int) ([]control.EscalationSummary, error) {
	return nil, nil
}
func (s *stubSession) Memory(context.Context) (control.MemoryView, error) {
	return control.MemoryView{}, nil
}
func (s *stubSession) Steering(context.Context) ([]control.SteeringRecord, error) { return nil, nil }
func (s *stubSession) Send(context.Context, control.ResearchCommand) error        { return nil }

func TestNewRejectsNilConfig(t *testing.T) {
	if _, err := harness.New(nil); err == nil {
		t.Fatal("New(nil) succeeded, want an error")
	}
}

// A harness with no usable work source is still a harness: it exists to
// watch a research programme just as much as to poll issues, and refusing
// to open would strand the user with no interface at all.
func TestStartIssueWorkflowToleratesNoWorkSource(t *testing.T) {
	h, err := harness.New(&config.Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := h.StartIssueWorkflow(ctx); err != nil {
		t.Fatalf("StartIssueWorkflow with no configured source: %v", err)
	}

	list, err := h.Host().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("session count = %d, want 0 before any run starts", len(list))
	}

	cancel()
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// Work started by another command must land in the same list, or the
// harness is not actually unified.
func TestAddSessionAppearsInTheHost(t *testing.T) {
	h, err := harness.New(&config.Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer h.Close() //nolint:errcheck

	ctx := context.Background()
	desc := session.Descriptor{
		ID: "research:arachne", Kind: session.KindResearch,
		Title: "arachne", State: control.RunStateRunning,
	}
	if err := h.AddSession(desc, &stubSession{desc: desc}); err != nil {
		t.Fatalf("AddSession: %v", err)
	}

	list, err := h.Host().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != "research:arachne" {
		t.Fatalf("List = %+v, want the added research session", list)
	}

	if _, err := h.Host().Open(ctx, "research:arachne"); err != nil {
		t.Fatalf("Open: %v", err)
	}
}

// The harness must be able to start work, not only watch it — and it must
// do so through the same pipeline, so a task typed into the interface is
// routed, isolated, and proved like any other.
func TestStartTaskRequiresALocalLane(t *testing.T) {
	h, err := harness.New(&config.Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer h.Close() //nolint:errcheck

	if _, err := h.StartTask(context.Background(), "do a thing"); err == nil {
		t.Fatal("StartTask without a lane succeeded, want an error")
	}
}

// A host that cannot start work must be unable to, not merely decline.
func TestHarnessSatisfiesStarter(t *testing.T) {
	h, err := harness.New(&config.Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer h.Close() //nolint:errcheck

	if _, ok := session.AsStarter(h); !ok {
		t.Fatal("harness does not satisfy session.Starter")
	}
	if _, ok := session.AsStarter(session.NewRegistry()); ok {
		t.Fatal("a bare registry must not be able to start work")
	}
}
