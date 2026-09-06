package research

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/haha-systems/ariadne/internal/memory"
)

// fakeStore is a hand-written fake memory.Store; no mocking framework,
// matching the repo's existing test conventions.
type fakeStore struct {
	frontier    memory.Record
	frontierErr error
	evidence    map[string]memory.Record
	evidenceErr error
	observed    []memory.ObserveInput
	acceptCalls []string
	rejectCalls []string
}

func (f *fakeStore) Observe(_ context.Context, in memory.ObserveInput) (memory.Record, error) {
	f.observed = append(f.observed, in)
	return memory.Record{ID: "rec", Kind: in.Kind, Project: in.Project, Subject: in.Subject, Summary: in.Summary}, nil
}
func (f *fakeStore) Retrieve(context.Context, memory.RetrieveInput) ([]memory.Record, error) {
	return nil, nil
}
func (f *fakeStore) Accept(_ context.Context, _, recordID, _, _ string) (memory.Record, error) {
	f.acceptCalls = append(f.acceptCalls, recordID)
	return memory.Record{ID: recordID, State: memory.StateAccepted}, nil
}
func (f *fakeStore) Reject(_ context.Context, _, recordID, _, _ string) (memory.Record, error) {
	f.rejectCalls = append(f.rejectCalls, recordID)
	return memory.Record{ID: recordID, State: memory.StateRejected}, nil
}
func (f *fakeStore) GetEvidence(_ context.Context, _ string, refs []string) ([]memory.Record, error) {
	if f.evidenceErr != nil {
		return nil, f.evidenceErr
	}
	out := make([]memory.Record, 0, len(refs))
	for _, ref := range refs {
		rec, ok := f.evidence[ref]
		if !ok {
			return nil, errors.New("not found: " + ref)
		}
		out = append(out, rec)
	}
	return out, nil
}
func (f *fakeStore) Frontier(context.Context, string) (memory.Record, error) {
	return f.frontier, f.frontierErr
}
func (f *fakeStore) WriteFrontier(_ context.Context, project, summary, agentID string) (memory.Record, error) {
	f.frontier = memory.Record{Project: project, Summary: summary, AgentID: agentID, Kind: memory.KindFrontier}
	return f.frontier, nil
}

func TestBuildPacket_IncludesFrontierAndEvidence(t *testing.T) {
	store := &fakeStore{
		frontier: memory.Record{Summary: "one-bit advantage explained by nonlinearity"},
		evidence: map[string]memory.Record{
			"ev-1": {ID: "ev-1", Kind: memory.KindObservation, Subject: "run-14", Summary: "parity holds"},
		},
	}

	packet, err := BuildPacket(context.Background(), store, "distributed-cognition", "Design the cheapest discriminator", []string{"ev-1"})
	if err != nil {
		t.Fatalf("BuildPacket() error = %v", err)
	}
	if packet.Question != "Design the cheapest discriminator" {
		t.Errorf("Question = %q", packet.Question)
	}
	if len(packet.Evidence) != 1 || packet.Evidence[0].ID != "ev-1" {
		t.Errorf("Evidence = %+v", packet.Evidence)
	}
	if packet.Frontier.Summary != "one-bit advantage explained by nonlinearity" {
		t.Errorf("Frontier = %+v", packet.Frontier)
	}
}

func TestBuildPacket_MissingFrontierIsNotAnError(t *testing.T) {
	store := &fakeStore{frontierErr: errors.New("no frontier recorded")}
	packet, err := BuildPacket(context.Background(), store, "p1", "question", nil)
	if err != nil {
		t.Fatalf("BuildPacket() error = %v, want nil (missing frontier is fine early in a project)", err)
	}
	if packet.Frontier.Summary != "" {
		t.Errorf("expected zero-value frontier, got %+v", packet.Frontier)
	}
}

func TestBuildPacket_EvidenceErrorWrapped(t *testing.T) {
	store := &fakeStore{evidenceErr: errors.New("boom")}
	_, err := BuildPacket(context.Background(), store, "p1", "q", []string{"ev-1"})
	if err == nil {
		t.Fatal("expected error to propagate from GetEvidence")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected wrapped error to mention underlying cause, got %v", err)
	}
}

func TestPacket_Render_IsBoundedLength(t *testing.T) {
	longSummary := strings.Repeat("x", maxFrontierChars*3)
	packet := Packet{
		Project:  "p1",
		Question: "q",
		Frontier: memory.Record{Summary: longSummary},
	}
	rendered := packet.Render()
	if len(rendered) > maxFrontierChars*2 {
		t.Errorf("Render() length = %d, want bounded well under the unbounded %d chars", len(rendered), len(longSummary))
	}
}

func TestPacket_Render_IncludesGoalAndTask(t *testing.T) {
	packet := Packet{Question: "What discriminates A from B?"}
	rendered := packet.Render()
	if !strings.Contains(rendered, "GOAL") || !strings.Contains(rendered, "What discriminates A from B?") {
		t.Errorf("Render() missing GOAL section: %q", rendered)
	}
	if !strings.Contains(rendered, "TASK") {
		t.Errorf("Render() missing TASK section: %q", rendered)
	}
}
