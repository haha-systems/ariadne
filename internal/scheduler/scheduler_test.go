package scheduler

import (
	"context"
	"testing"

	"github.com/haha-systems/ariadne/internal/agentprofile"
)

func TestScheduler_Decide_PicksPlentifulWithoutContext(t *testing.T) {
	s := New(ControlAutomatic, NewBudget(0, 0, 0), nil, "astra")
	options := []Action{
		{Name: "continue", ProfileName: "ari", ScarcityClass: agentprofile.ScarcityPlentiful, Weight: 0.9},
		{Name: "escalate", ProfileName: "astra", ScarcityClass: agentprofile.ScarcityRare, Weight: 0.1},
	}
	// No Context needed: the best-weighted option is plentiful, so this
	// isn't an escalation at all.
	dec, err := s.Decide(context.Background(), DecisionRequest{Options: options})
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if dec.Escalated || dec.Chosen.ProfileName != "ari" {
		t.Errorf("Decide() = %+v, want continuing with ari, no escalation", dec)
	}
}

func TestScheduler_Decide_HighestWeightWinsAmongSameScarcity(t *testing.T) {
	s := New(ControlAutomatic, NewBudget(0, 0, 0), nil, "")
	options := []Action{
		{Name: "a", ProfileName: "a", ScarcityClass: agentprofile.ScarcityPlentiful, Weight: 0.3},
		{Name: "b", ProfileName: "b", ScarcityClass: agentprofile.ScarcityPlentiful, Weight: 0.7},
		{Name: "c", ProfileName: "c", ScarcityClass: agentprofile.ScarcityPlentiful, Weight: 0.5},
	}
	dec, err := s.Decide(context.Background(), DecisionRequest{Options: options})
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if dec.Chosen.ProfileName != "b" || dec.Score != 0.7 {
		t.Errorf("Decide() = %+v, want the highest-weight option 'b' (0.7)", dec)
	}
}

func TestScheduler_Decide_EscalationRequiresNonEmptyContext(t *testing.T) {
	s := New(ControlAutomatic, NewBudget(0, 0, 0), nil, "astra")
	options := []Action{
		{Name: "continue", ProfileName: "ari", ScarcityClass: agentprofile.ScarcityPlentiful, Weight: 0.1},
		{Name: "escalate", ProfileName: "astra", ScarcityClass: agentprofile.ScarcityRare, Weight: 0.9},
	}
	// Context is empty even though the best option is scarce — must error
	// rather than silently escalate with no stated reason.
	if _, err := s.Decide(context.Background(), DecisionRequest{Options: options}); err == nil {
		t.Fatal("expected an error when escalating with an empty Context")
	}
}

func TestScheduler_Decide_NoOptionsIsError(t *testing.T) {
	s := New(ControlAutomatic, NewBudget(0, 0, 0), nil, "astra")
	if _, err := s.Decide(context.Background(), DecisionRequest{Context: "x"}); err == nil {
		t.Fatal("expected an error when no candidate actions are provided")
	}
}

func TestScheduler_Decide_DeniedEscalationWithNoFallbackIsError(t *testing.T) {
	s := New(ControlDisabled, NewBudget(0, 0, 0), nil, "astra")
	options := []Action{
		{Name: "escalate", ProfileName: "astra", ScarcityClass: agentprofile.ScarcityRare, Weight: 0.9},
	}
	if _, err := s.Decide(context.Background(), DecisionRequest{Context: "x", Options: options}); err == nil {
		t.Fatal("expected an error when escalation is denied and no plentiful fallback exists")
	}
}

func TestScheduler_Decide_RecursionGuardTakesPrecedence(t *testing.T) {
	s := New(ControlAutomatic, NewBudget(0, 0, 0), nil, "astra")
	options := []Action{
		{Name: "escalate again", ProfileName: "astra", ScarcityClass: agentprofile.ScarcityRare, Weight: 0.9},
	}
	_, err := s.Decide(context.Background(), DecisionRequest{Context: "x", Depth: 1, Options: options})
	if err == nil {
		t.Fatal("expected the recursion guard to reject offering astra at depth 1")
	}
}

func TestScheduler_Decide_ThresholdBoundary_TableDriven(t *testing.T) {
	cases := []struct {
		name         string
		ariWeight    float64
		astraWeight  float64
		wantEscalate bool
	}{
		{"ari clearly stronger", 0.9, 0.1, false},
		{"tied favors first evaluated (ari)", 0.5, 0.5, false},
		{"astra barely stronger", 0.5, 0.51, true},
		{"astra clearly stronger", 0.1, 0.9, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(ControlAutomatic, NewBudget(0, 0, 0), nil, "astra")
			options := []Action{
				{Name: "continue", ProfileName: "ari", ScarcityClass: agentprofile.ScarcityPlentiful, Weight: tc.ariWeight},
				{Name: "escalate", ProfileName: "astra", ScarcityClass: agentprofile.ScarcityRare, Weight: tc.astraWeight},
			}
			dec, err := s.Decide(context.Background(), DecisionRequest{Context: "boundary test", Options: options})
			if err != nil {
				t.Fatalf("Decide() error = %v", err)
			}
			if dec.Escalated != tc.wantEscalate {
				t.Errorf("Escalated = %v, want %v (decision: %+v)", dec.Escalated, tc.wantEscalate, dec)
			}
		})
	}
}
