package scheduler

import (
	"context"
	"testing"

	"github.com/haha-systems/ariadne/internal/agentprofile"
)

// fakeApprover is a hand-written fake Approver; no mocking framework,
// matching the repo's existing test conventions.
type fakeApprover struct {
	approved bool
	reason   string
	err      error
	calls    int
}

func (a *fakeApprover) RequestApproval(context.Context, DecisionRequest) (bool, string, error) {
	a.calls++
	return a.approved, a.reason, a.err
}

func scarceVsPlentiful() []Action {
	return []Action{
		{Name: "continue", ProfileName: "ari", ScarcityClass: agentprofile.ScarcityPlentiful, Weight: 0.1},
		{Name: "escalate", ProfileName: "astra", ScarcityClass: agentprofile.ScarcityRare, Weight: 0.9},
	}
}

func TestScheduler_ControlAutomatic_ApprovesWithoutApprover(t *testing.T) {
	s := New(ControlAutomatic, NewBudget(0, 0, 0), nil, "astra")
	dec, err := s.Decide(context.Background(), DecisionRequest{Context: "two hypotheses survive", Options: scarceVsPlentiful()})
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if !dec.Escalated || dec.Chosen.ProfileName != "astra" {
		t.Errorf("Decide() = %+v, want escalation to astra", dec)
	}
}

func TestScheduler_ControlAutomatic_DeniedWhenBudgetExhausted(t *testing.T) {
	exhausted := NewBudget(1, 0, 0)
	exhausted.RecordEscalation()
	s := New(ControlAutomatic, exhausted, nil, "astra")

	dec, err := s.Decide(context.Background(), DecisionRequest{Context: "two hypotheses survive", Options: scarceVsPlentiful()})
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if dec.Escalated {
		t.Errorf("expected denial to fall back rather than escalate, got %+v", dec)
	}
	if dec.Chosen.ProfileName != "ari" {
		t.Errorf("expected fallback to the plentiful action, got %+v", dec)
	}
	if dec.Reason == "" {
		t.Error("expected a non-empty denial reason (no hidden escalation reasons)")
	}
}

func TestScheduler_ControlManual_RequiresApprover(t *testing.T) {
	s := New(ControlManual, NewBudget(0, 0, 0), nil, "astra")
	dec, err := s.Decide(context.Background(), DecisionRequest{Context: "needs review", Options: scarceVsPlentiful()})
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if dec.Escalated {
		t.Error("expected no escalation without a configured approver")
	}
}

func TestScheduler_ControlManual_ApproverApproves(t *testing.T) {
	approver := &fakeApprover{approved: true}
	s := New(ControlManual, NewBudget(0, 0, 0), approver, "astra")
	dec, err := s.Decide(context.Background(), DecisionRequest{Context: "needs review", Options: scarceVsPlentiful()})
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if !dec.Escalated {
		t.Error("expected escalation once the approver approves")
	}
	if approver.calls != 1 {
		t.Errorf("expected exactly one approval request, got %d", approver.calls)
	}
}

func TestScheduler_ControlManual_ApproverDenies(t *testing.T) {
	approver := &fakeApprover{approved: false, reason: "not worth the quota yet"}
	s := New(ControlManual, NewBudget(0, 0, 0), approver, "astra")
	dec, err := s.Decide(context.Background(), DecisionRequest{Context: "needs review", Options: scarceVsPlentiful()})
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if dec.Escalated {
		t.Error("expected denial to be respected")
	}
	if dec.Reason != "not worth the quota yet" {
		t.Errorf("Reason = %q, want the approver's stated reason", dec.Reason)
	}
}

func TestScheduler_ControlDisabled_AlwaysDenies(t *testing.T) {
	approver := &fakeApprover{approved: true} // even a would-be-approving approver must not be consulted
	s := New(ControlDisabled, NewBudget(0, 0, 0), approver, "astra")
	dec, err := s.Decide(context.Background(), DecisionRequest{Context: "needs review", Options: scarceVsPlentiful()})
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if dec.Escalated {
		t.Error("expected ControlDisabled to always deny escalation")
	}
	if approver.calls != 0 {
		t.Error("expected ControlDisabled to never consult the approver")
	}
}
