package ghostdive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_Observe_SendsExpectedRequest(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id": "rec-1", "layer": "episodic", "predicate": "observes",
			"promotion_state": "observed", "subject": "s", "summary": "sum",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	rec, err := c.Observe(context.Background(), "space-1", ObserveRequest{
		Layer: "episodic", Subject: "s", Predicate: "observes", Summary: "sum",
		Confidence: 0.5, AgentID: "ari", EvidenceRefs: []string{"e1"}, SkipAutoSupersede: true,
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if rec.ID != "rec-1" {
		t.Errorf("rec.ID = %q, want rec-1", rec.ID)
	}
	if gotPath != "/v1/spaces/space-1/memories" {
		t.Errorf("path = %q, want /v1/spaces/space-1/memories", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	for _, field := range []string{"layer", "subject", "predicate", "object", "summary", "confidence", "agent_id", "provenance", "evidence_refs", "skip_auto_supersede"} {
		if _, ok := gotBody[field]; !ok {
			t.Errorf("request body missing field %q: %v", field, gotBody)
		}
	}
	if gotBody["skip_auto_supersede"] != true {
		t.Errorf("skip_auto_supersede = %v, want true", gotBody["skip_auto_supersede"])
	}
}

func TestClient_Retrieve_SendsExpectedRequestAndParsesHits(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"hits": []map[string]any{
				{"id": "rec-1", "score": 0.9},
				{"id": "rec-2", "score": 0.4},
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	hits, err := c.Retrieve(context.Background(), "space-1", RetrieveRequest{
		Query: "parity", TopK: 5, Mode: "hybrid", Layers: []string{"episodic"}, States: []string{"observed"},
	})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(hits) != 2 || hits[0].ID != "rec-1" || hits[0].Score != 0.9 {
		t.Fatalf("Retrieve() = %+v", hits)
	}
	for _, field := range []string{"query", "top_k", "mode", "layers", "states"} {
		if _, ok := gotBody[field]; !ok {
			t.Errorf("request body missing field %q: %v", field, gotBody)
		}
	}
}

func TestClient_Transition_SendsExpectedRequest(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"record":     map[string]any{"id": "rec-1", "promotion_state": "accepted"},
			"transition": map[string]any{"id": "tr-1", "to_state": "accepted"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	rec, tr, err := c.Transition(context.Background(), "space-1", "rec-1", TransitionRequest{
		ToState: "accepted", Reason: "confirmed", AgentID: "astra",
	})
	if err != nil {
		t.Fatalf("Transition() error = %v", err)
	}
	if rec.PromotionState != "accepted" || tr.ToState != "accepted" {
		t.Errorf("Transition() = rec=%+v tr=%+v", rec, tr)
	}
	if gotPath != "/v1/spaces/space-1/memories/rec-1/transitions" {
		t.Errorf("path = %q", gotPath)
	}
	for _, field := range []string{"to_state", "reason", "agent_id"} {
		if _, ok := gotBody[field]; !ok {
			t.Errorf("request body missing field %q: %v", field, gotBody)
		}
	}
}

func TestClient_GetRecord_UsesGET(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{"id": "rec-1"}) //nolint:errcheck
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	rec, err := c.GetRecord(context.Background(), "space-1", "rec-1")
	if err != nil {
		t.Fatalf("GetRecord() error = %v", err)
	}
	if rec.ID != "rec-1" {
		t.Errorf("rec.ID = %q", rec.ID)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/v1/spaces/space-1/memories/rec-1" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestClient_ErrorResponse_DecodedAsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"error": "quota exceeded", "code": "quota_exceeded", "request_id": "req-1",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.GetRecord(context.Background(), "space-1", "rec-1")
	if err == nil {
		t.Fatal("expected an error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests || apiErr.Code != "quota_exceeded" || apiErr.RequestID != "req-1" {
		t.Errorf("APIError = %+v", apiErr)
	}
}
