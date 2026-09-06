package inmemory

import (
	"context"
	"strings"
	"testing"

	"github.com/haha-systems/ariadne/internal/memory"
)

func TestStore_Observe_SetsInitialStateByKind(t *testing.T) {
	cases := []struct {
		kind      memory.Kind
		wantState string
	}{
		{memory.KindObservation, memory.StateObserved},
		{memory.KindHypothesis, memory.StateCandidate},
		{memory.KindDecision, memory.StateAccepted},
		{memory.KindEscalation, memory.StateObserved},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			s := New()
			rec, err := s.Observe(context.Background(), memory.ObserveInput{
				Project: "distributed-cognition", Subject: "x", Summary: "y", Kind: tc.kind,
			})
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if rec.State != tc.wantState {
				t.Errorf("State = %q, want %q", rec.State, tc.wantState)
			}
			if rec.ID == "" {
				t.Error("expected non-empty ID")
			}
		})
	}
}

func TestStore_Observe_RequiresProject(t *testing.T) {
	s := New()
	_, err := s.Observe(context.Background(), memory.ObserveInput{Kind: memory.KindObservation})
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestStore_Observe_RejectsFrontierKind(t *testing.T) {
	s := New()
	_, err := s.Observe(context.Background(), memory.ObserveInput{Project: "p", Kind: memory.KindFrontier})
	if err == nil {
		t.Fatal("expected error — frontier records must go through WriteFrontier")
	}
}

func TestStore_Retrieve_FiltersByProjectKindStateQuery(t *testing.T) {
	s := New()
	ctx := context.Background()
	obs, _ := s.Observe(ctx, memory.ObserveInput{Project: "p1", Subject: "run-14", Summary: "parity holds", Kind: memory.KindObservation})
	hyp, _ := s.Observe(ctx, memory.ObserveInput{Project: "p1", Subject: "nonlinearity", Summary: "explains parity", Kind: memory.KindHypothesis})
	s.Observe(ctx, memory.ObserveInput{Project: "p2", Subject: "other project", Summary: "irrelevant", Kind: memory.KindObservation}) //nolint:errcheck

	// Project filter.
	got, err := s.Retrieve(ctx, memory.RetrieveInput{Project: "p1"})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records for p1, got %d", len(got))
	}

	// Kind filter.
	got, err = s.Retrieve(ctx, memory.RetrieveInput{Project: "p1", Kinds: []memory.Kind{memory.KindHypothesis}})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != hyp.ID {
		t.Fatalf("expected only the hypothesis, got %+v", got)
	}

	// Query filter (case-insensitive substring over subject+summary).
	got, err = s.Retrieve(ctx, memory.RetrieveInput{Project: "p1", Query: "PARITY"})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected both records to match 'parity', got %d", len(got))
	}

	// State filter, after rejecting the hypothesis.
	if _, err := s.Reject(ctx, "p1", hyp.ID, "explained by nonlinearity elsewhere", "ari"); err != nil {
		t.Fatalf("Reject() error = %v", err)
	}
	got, err = s.Retrieve(ctx, memory.RetrieveInput{Project: "p1", States: []string{memory.StateRejected}})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != hyp.ID {
		t.Fatalf("expected only the rejected hypothesis, got %+v", got)
	}

	// TopK limit.
	got, err = s.Retrieve(ctx, memory.RetrieveInput{Project: "p1", TopK: 1})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected TopK=1 to limit to 1 record, got %d", len(got))
	}
	_ = obs
}

func TestStore_RejectedHypothesesSurviveAsGraveyard(t *testing.T) {
	s := New()
	ctx := context.Background()
	hyp, _ := s.Observe(ctx, memory.ObserveInput{Project: "p1", Subject: "one-bit-messages", Summary: "uniquely responsible for parity success", Kind: memory.KindHypothesis})

	if _, err := s.Reject(ctx, "p1", hyp.ID, "effect explained by continuous nonlinearity", "astra"); err != nil {
		t.Fatalf("Reject() error = %v", err)
	}

	got, err := s.Retrieve(ctx, memory.RetrieveInput{Project: "p1", Kinds: []memory.Kind{memory.KindHypothesis}, States: []string{memory.StateRejected}})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != hyp.ID {
		t.Fatalf("expected the rejected hypothesis still retrievable, got %+v", got)
	}
}

func TestStore_Accept_TransitionsAndCreatesDecision(t *testing.T) {
	s := New()
	ctx := context.Background()
	hyp, _ := s.Observe(ctx, memory.ObserveInput{Project: "p1", Subject: "explicit-local-objectives", Summary: "preserve differentiation", Kind: memory.KindHypothesis})

	accepted, err := s.Accept(ctx, "p1", hyp.ID, "confirmed by ablation E14", "astra")
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	if accepted.State != memory.StateAccepted {
		t.Errorf("hypothesis State = %q, want accepted", accepted.State)
	}

	decisions, err := s.Retrieve(ctx, memory.RetrieveInput{Project: "p1", Kinds: []memory.Kind{memory.KindDecision}})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision record, got %d", len(decisions))
	}
	if len(decisions[0].EvidenceRefs) != 1 || decisions[0].EvidenceRefs[0] != hyp.ID {
		t.Errorf("decision EvidenceRefs = %v, want [%s]", decisions[0].EvidenceRefs, hyp.ID)
	}
	if !strings.Contains(decisions[0].Summary, "ablation E14") {
		t.Errorf("decision Summary = %q, want it to contain the acceptance reason", decisions[0].Summary)
	}
}

func TestStore_Accept_UnknownRecord(t *testing.T) {
	s := New()
	if _, err := s.Accept(context.Background(), "p1", "does-not-exist", "why", "ari"); err == nil {
		t.Fatal("expected error for unknown record")
	}
}

func TestStore_Accept_WrongProjectFailsFast(t *testing.T) {
	s := New()
	ctx := context.Background()
	hyp, _ := s.Observe(ctx, memory.ObserveInput{Project: "p1", Subject: "x", Kind: memory.KindHypothesis})
	if _, err := s.Accept(ctx, "p2", hyp.ID, "why", "ari"); err == nil {
		t.Fatal("expected error when accepting a record under the wrong project")
	}
}

func TestStore_GetEvidence_OrderedAndFailsFast(t *testing.T) {
	s := New()
	ctx := context.Background()
	a, _ := s.Observe(ctx, memory.ObserveInput{Project: "p1", Subject: "a", Kind: memory.KindObservation})
	b, _ := s.Observe(ctx, memory.ObserveInput{Project: "p1", Subject: "b", Kind: memory.KindObservation})

	got, err := s.GetEvidence(ctx, "p1", []string{b.ID, a.ID})
	if err != nil {
		t.Fatalf("GetEvidence() error = %v", err)
	}
	if len(got) != 2 || got[0].ID != b.ID || got[1].ID != a.ID {
		t.Fatalf("expected order preserved [b,a], got %+v", got)
	}

	if _, err := s.GetEvidence(ctx, "p1", []string{a.ID, "missing-ref"}); err == nil {
		t.Fatal("expected error for missing evidence ref")
	}
}

func TestStore_GetEvidence_WrongProjectFailsFast(t *testing.T) {
	s := New()
	ctx := context.Background()
	a, _ := s.Observe(ctx, memory.ObserveInput{Project: "p1", Subject: "a", Kind: memory.KindObservation})

	if _, err := s.GetEvidence(ctx, "p2", []string{a.ID}); err == nil {
		t.Fatal("expected error when fetching a record under the wrong project")
	}
}

func TestStore_Frontier_WriteThenRead(t *testing.T) {
	s := New()
	ctx := context.Background()

	if _, err := s.Frontier(ctx, "p1"); err == nil {
		t.Fatal("expected error before any frontier is written")
	}

	first, err := s.WriteFrontier(ctx, "p1", "initial frontier", "ari")
	if err != nil {
		t.Fatalf("WriteFrontier() error = %v", err)
	}
	got, err := s.Frontier(ctx, "p1")
	if err != nil {
		t.Fatalf("Frontier() error = %v", err)
	}
	if got.ID != first.ID || got.Summary != "initial frontier" {
		t.Errorf("Frontier() = %+v, want the just-written record", got)
	}

	second, err := s.WriteFrontier(ctx, "p1", "updated frontier", "ari")
	if err != nil {
		t.Fatalf("WriteFrontier() error = %v", err)
	}
	got, err = s.Frontier(ctx, "p1")
	if err != nil {
		t.Fatalf("Frontier() error = %v", err)
	}
	if got.ID != second.ID || got.Summary != "updated frontier" {
		t.Errorf("Frontier() after second write = %+v, want the latest record", got)
	}
}

// TestStore_WriteFrontierNeverOverwritesObservations is a direct regression
// test for guardrail §23/§51: a mutable Frontier summary must never
// overwrite or shadow append-only records, however many times it is
// rewritten for the same project.
func TestStore_WriteFrontierNeverOverwritesObservations(t *testing.T) {
	s := New()
	ctx := context.Background()

	obs, err := s.Observe(ctx, memory.ObserveInput{Project: "p1", Subject: "run-14", Summary: "parity holds", Kind: memory.KindObservation})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	hyp, err := s.Observe(ctx, memory.ObserveInput{Project: "p1", Subject: "one-bit-messages", Summary: "uniquely responsible", Kind: memory.KindHypothesis})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if _, err := s.Reject(ctx, "p1", hyp.ID, "explained by nonlinearity", "astra"); err != nil {
		t.Fatalf("Reject() error = %v", err)
	}

	for i := 0; i < 5; i++ {
		if _, err := s.WriteFrontier(ctx, "p1", "frontier summary", "ari"); err != nil {
			t.Fatalf("WriteFrontier() error = %v", err)
		}
	}

	all, err := s.Retrieve(ctx, memory.RetrieveInput{Project: "p1"})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	// The observation, the hypothesis (still present, now rejected), plus
	// nothing else — Retrieve() must never surface frontier records, and
	// repeated frontier writes must not have mutated or removed either.
	foundObs, foundHyp := false, false
	for _, rec := range all {
		if rec.ID == obs.ID {
			foundObs = true
			if rec.Summary != "parity holds" {
				t.Errorf("observation Summary mutated: %q", rec.Summary)
			}
		}
		if rec.ID == hyp.ID {
			foundHyp = true
			if rec.State != memory.StateRejected {
				t.Errorf("hypothesis State changed by frontier writes: %q", rec.State)
			}
		}
		if rec.Kind == memory.KindFrontier {
			t.Errorf("Retrieve() must never return frontier records, got %+v", rec)
		}
	}
	if !foundObs || !foundHyp {
		t.Fatalf("expected both original records to survive, foundObs=%v foundHyp=%v", foundObs, foundHyp)
	}
}
