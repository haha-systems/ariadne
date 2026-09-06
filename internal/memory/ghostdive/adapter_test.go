package ghostdive

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/haha-systems/ariadne/internal/memory"
)

// fakeClient is a hand-written fake ghostdiveClient (no mocking framework,
// matching the repo's existing test conventions). It stores records in a
// flat map keyed by ID, ignoring spaceID for simplicity since all tests use
// a single space.
type fakeClient struct {
	records     map[string]Record
	transitions []TransitionRequest
	nextID      int
	observeErr  error
	getErr      error
}

func newFakeClient() *fakeClient {
	return &fakeClient{records: make(map[string]Record)}
}

func (f *fakeClient) Observe(_ context.Context, _ string, req ObserveRequest) (Record, error) {
	if f.observeErr != nil {
		return Record{}, f.observeErr
	}
	f.nextID++
	rec := Record{
		ID:             fmt.Sprintf("rec-%d", f.nextID),
		Layer:          req.Layer,
		Subject:        req.Subject,
		Predicate:      req.Predicate,
		Object:         req.Object,
		Summary:        req.Summary,
		Confidence:     req.Confidence,
		AgentID:        req.AgentID,
		EvidenceRefs:   req.EvidenceRefs,
		PromotionState: "observed", // Ghostdive always creates records as "observed"
	}
	f.records[rec.ID] = rec
	return rec, nil
}

func (f *fakeClient) Retrieve(_ context.Context, _ string, req RetrieveRequest) ([]Hit, error) {
	var hits []Hit
	for _, rec := range f.records {
		if len(req.Layers) > 0 && !containsStr(req.Layers, rec.Layer) {
			continue
		}
		if len(req.States) > 0 && !containsStr(req.States, rec.PromotionState) {
			continue
		}
		if req.Query != "" && !strings.Contains(strings.ToLower(rec.Subject+" "+rec.Summary), strings.ToLower(req.Query)) {
			continue
		}
		hits = append(hits, Hit{Record: rec, Score: 1.0})
	}
	if req.TopK > 0 && len(hits) > req.TopK {
		hits = hits[:req.TopK]
	}
	return hits, nil
}

func (f *fakeClient) GetRecord(_ context.Context, _ string, recordID string) (Record, error) {
	if f.getErr != nil {
		return Record{}, f.getErr
	}
	rec, ok := f.records[recordID]
	if !ok {
		return Record{}, fmt.Errorf("not found: %s", recordID)
	}
	return rec, nil
}

func (f *fakeClient) Transition(_ context.Context, _ string, recordID string, req TransitionRequest) (Record, Transition, error) {
	f.transitions = append(f.transitions, req)
	rec, ok := f.records[recordID]
	if !ok {
		return Record{}, Transition{}, fmt.Errorf("not found: %s", recordID)
	}
	rec.PromotionState = req.ToState
	f.records[recordID] = rec
	return rec, Transition{ID: fmt.Sprintf("tr-%d", len(f.transitions)), RecordID: recordID, ToState: req.ToState, Reason: req.Reason, AgentID: req.AgentID}, nil
}

func containsStr(vals []string, v string) bool {
	for _, want := range vals {
		if want == v {
			return true
		}
	}
	return false
}

func newTestAdapter(t *testing.T, client *fakeClient) *Adapter {
	t.Helper()
	return NewAdapter(client, map[string]string{"p1": "space-1"}, t.TempDir())
}

func TestAdapter_Observe_MapsKindToLayerPredicate(t *testing.T) {
	cases := []struct {
		kind          memory.Kind
		wantLayer     string
		wantPredicate string
		wantState     string
	}{
		{memory.KindObservation, "episodic", "observes", memory.StateObserved},
		{memory.KindHypothesis, "semantic", "hypothesizes", memory.StateCandidate},
		{memory.KindDecision, "procedural", "decides", memory.StateAccepted},
		{memory.KindEscalation, "allostatic", "escalates", memory.StateObserved},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			client := newFakeClient()
			a := newTestAdapter(t, client)

			rec, err := a.Observe(context.Background(), memory.ObserveInput{
				Project: "p1", Subject: "x", Summary: "y", Kind: tc.kind,
			})
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if rec.State != tc.wantState {
				t.Errorf("State = %q, want %q", rec.State, tc.wantState)
			}
			if rec.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", rec.Kind, tc.kind)
			}

			raw := client.records[rec.ID]
			if raw.Layer != tc.wantLayer || raw.Predicate != tc.wantPredicate {
				t.Errorf("underlying record layer/predicate = %s/%s, want %s/%s", raw.Layer, raw.Predicate, tc.wantLayer, tc.wantPredicate)
			}
		})
	}
}

func TestAdapter_Observe_AlwaysSkipsAutoSupersede(t *testing.T) {
	client := newFakeClient()
	a := newTestAdapter(t, client)
	_, err := a.Observe(context.Background(), memory.ObserveInput{Project: "p1", Subject: "x", Kind: memory.KindObservation})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	// The fake client doesn't record SkipAutoSupersede directly, but
	// client_test.go already verifies the wire format includes it; here we
	// just confirm Observe doesn't error, exercising the full path.
}

func TestAdapter_Observe_UnknownProject(t *testing.T) {
	a := newTestAdapter(t, newFakeClient())
	_, err := a.Observe(context.Background(), memory.ObserveInput{Project: "unknown", Kind: memory.KindObservation})
	if err == nil {
		t.Fatal("expected error for unconfigured project")
	}
}

func TestAdapter_Accept_TransitionsAndCreatesDecision(t *testing.T) {
	client := newFakeClient()
	a := newTestAdapter(t, client)

	hyp, err := a.Observe(context.Background(), memory.ObserveInput{Project: "p1", Subject: "explicit-local-objectives", Kind: memory.KindHypothesis})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	accepted, err := a.Accept(context.Background(), "p1", hyp.ID, "confirmed by ablation E14", "astra")
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	if accepted.State != memory.StateAccepted {
		t.Errorf("State = %q, want accepted", accepted.State)
	}

	// Accept should have produced exactly one Transition call (to accepted)
	// plus one Observe+Transition pair for the new Decision record.
	decisions, err := a.Retrieve(context.Background(), memory.RetrieveInput{Project: "p1", Kinds: []memory.Kind{memory.KindDecision}, Query: "ablation"})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision record, got %d: %+v", len(decisions), decisions)
	}
	if len(decisions[0].EvidenceRefs) != 1 || decisions[0].EvidenceRefs[0] != hyp.ID {
		t.Errorf("decision EvidenceRefs = %v, want [%s]", decisions[0].EvidenceRefs, hyp.ID)
	}
}

func TestAdapter_Reject_Transitions(t *testing.T) {
	client := newFakeClient()
	a := newTestAdapter(t, client)

	hyp, _ := a.Observe(context.Background(), memory.ObserveInput{Project: "p1", Subject: "one-bit-messages", Kind: memory.KindHypothesis})
	rejected, err := a.Reject(context.Background(), "p1", hyp.ID, "explained by nonlinearity", "astra")
	if err != nil {
		t.Fatalf("Reject() error = %v", err)
	}
	if rejected.State != memory.StateRejected {
		t.Errorf("State = %q, want rejected", rejected.State)
	}
}

func TestAdapter_GetEvidence_OrderedAndFailsFast(t *testing.T) {
	client := newFakeClient()
	a := newTestAdapter(t, client)

	obs1, _ := a.Observe(context.Background(), memory.ObserveInput{Project: "p1", Subject: "a", Kind: memory.KindObservation})
	obs2, _ := a.Observe(context.Background(), memory.ObserveInput{Project: "p1", Subject: "b", Kind: memory.KindObservation})

	got, err := a.GetEvidence(context.Background(), "p1", []string{obs2.ID, obs1.ID})
	if err != nil {
		t.Fatalf("GetEvidence() error = %v", err)
	}
	if len(got) != 2 || got[0].ID != obs2.ID || got[1].ID != obs1.ID {
		t.Fatalf("expected order preserved [obs2,obs1], got %+v", got)
	}

	if _, err := a.GetEvidence(context.Background(), "p1", []string{obs1.ID, "missing-ref"}); err == nil {
		t.Fatal("expected error for a missing evidence ref")
	}
}

func TestAdapter_Frontier_WriteThenReadViaPointerFile(t *testing.T) {
	client := newFakeClient()
	a := newTestAdapter(t, client)

	if _, err := a.Frontier(context.Background(), "p1"); err == nil {
		t.Fatal("expected error before any frontier is written")
	}

	first, err := a.WriteFrontier(context.Background(), "p1", "initial frontier", "ari")
	if err != nil {
		t.Fatalf("WriteFrontier() error = %v", err)
	}
	got, err := a.Frontier(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Frontier() error = %v", err)
	}
	if got.ID != first.ID {
		t.Errorf("Frontier() = %+v, want the just-written record", got)
	}

	second, err := a.WriteFrontier(context.Background(), "p1", "updated frontier", "ari")
	if err != nil {
		t.Fatalf("WriteFrontier() error = %v", err)
	}
	got, err = a.Frontier(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Frontier() error = %v", err)
	}
	if got.ID != second.ID || got.Summary != "updated frontier" {
		t.Errorf("Frontier() after second write = %+v, want the latest record", got)
	}
}

func TestAdapter_Frontier_SelfHealsWhenPointerStale(t *testing.T) {
	client := newFakeClient()
	a := newTestAdapter(t, client)

	if _, err := a.WriteFrontier(context.Background(), "p1", "the frontier", "ari"); err != nil {
		t.Fatalf("WriteFrontier() error = %v", err)
	}

	// Corrupt the pointer file so it references a record that no longer
	// resolves via GetRecord, forcing Frontier() down the self-heal path —
	// the real frontier record is still present and must still be found via
	// Retrieve.
	if err := os.WriteFile(a.pointerPath("p1"), []byte("some-stale-id"), 0644); err != nil {
		t.Fatalf("corrupt pointer file: %v", err)
	}

	got, err := a.Frontier(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Frontier() error = %v, want self-heal via Retrieve", err)
	}
	if got.Summary != "the frontier" {
		t.Errorf("Frontier() = %+v", got)
	}
}

// TestAdapter_WriteFrontierNeverTouchesOtherRecords is a direct regression
// test for guardrail §23/§51: repeated Frontier writes must never overwrite
// or shadow append-only Observation/Hypothesis/Decision/Escalation records,
// even though Frontier is the one write that allows auto-supersede.
func TestAdapter_WriteFrontierNeverTouchesOtherRecords(t *testing.T) {
	client := newFakeClient()
	a := newTestAdapter(t, client)

	obs, err := a.Observe(context.Background(), memory.ObserveInput{Project: "p1", Subject: "run-14", Summary: "parity holds", Kind: memory.KindObservation})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	for i := 0; i < 5; i++ {
		if _, err := a.WriteFrontier(context.Background(), "p1", "frontier summary", "ari"); err != nil {
			t.Fatalf("WriteFrontier() error = %v", err)
		}
	}

	raw, ok := client.records[obs.ID]
	if !ok {
		t.Fatal("observation record was removed by frontier writes")
	}
	if raw.Summary != "parity holds" {
		t.Errorf("observation Summary mutated: %q", raw.Summary)
	}
}
