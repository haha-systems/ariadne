package research

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/haha-systems/ariadne/internal/invocation"
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

	req := invocation.Escalation{Question: "Design the cheapest discriminator", EvidenceRefs: []string{"ev-1"}}
	packet, err := BuildPacket(context.Background(), store, "distributed-cognition", req, "", "")
	if err != nil {
		t.Fatalf("BuildPacket() error = %v", err)
	}
	if packet.Request.Question != "Design the cheapest discriminator" {
		t.Errorf("Question = %q", packet.Request.Question)
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
	packet, err := BuildPacket(context.Background(), store, "p1", invocation.Escalation{Question: "question"}, "", "")
	if err != nil {
		t.Fatalf("BuildPacket() error = %v, want nil (missing frontier is fine early in a project)", err)
	}
	if packet.Frontier.Summary != "" {
		t.Errorf("expected zero-value frontier, got %+v", packet.Frontier)
	}
}

func TestBuildPacket_EvidenceErrorWrapped(t *testing.T) {
	store := &fakeStore{evidenceErr: errors.New("boom")}
	_, err := BuildPacket(context.Background(), store, "p1", invocation.Escalation{Question: "q", EvidenceRefs: []string{"ev-1"}}, "", "")
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
		Request:  invocation.Escalation{Question: "q"},
		Frontier: memory.Record{Summary: longSummary},
	}
	rendered := packet.Render()
	// The frontier is the unbounded input; the rest of the prompt is fixed
	// scaffolding, so assert on what the bound actually protects.
	if strings.Contains(rendered, longSummary) {
		t.Error("Render() embedded the full untruncated frontier")
	}
	if !strings.Contains(rendered, "…") {
		t.Error("Render() should mark the frontier as truncated")
	}
	if len(rendered) > len(longSummary) {
		t.Errorf("Render() length = %d, want less than the unbounded %d chars", len(rendered), len(longSummary))
	}
}

func TestPacket_Render_IncludesScopeNoticeAndGuidanceAndProtocol(t *testing.T) {
	packet := Packet{Request: invocation.Escalation{Question: "q"}, Guidance: "Team mandate: characterize the parity effect."}
	rendered := packet.Render()
	if !strings.Contains(rendered, "SCOPE:") {
		t.Error("expected the built-in scope notice to always be present")
	}
	if !strings.Contains(rendered, "Team mandate: characterize the parity effect.") {
		t.Error("expected project guidance to be included")
	}
	if !strings.Contains(rendered, "$ARIADNE_OUTPUT_FILE") {
		t.Error("expected the envelope reporting protocol to be included")
	}
}

func TestPacket_Render_IncludesEscalatedQuestionAndTask(t *testing.T) {
	packet := Packet{Request: invocation.Escalation{Question: "What discriminates A from B?"}}
	rendered := packet.Render()
	if !strings.Contains(rendered, "ESCALATED QUESTION") || !strings.Contains(rendered, "What discriminates A from B?") {
		t.Errorf("Render() missing escalated question: %q", rendered)
	}
	if !strings.Contains(rendered, "TASK") {
		t.Errorf("Render() missing TASK section: %q", rendered)
	}
}
