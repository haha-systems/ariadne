// Package ghostdive implements memory.Store against a Ghostdive instance —
// a governed multi-layer memory store. Ghostdive has no built-in notion of
// "hypothesis," "decision," "escalation," or "frontier"; adapter.go maps
// those onto Ghostdive's Layer/Subject/Predicate/PromotionState primitives
// (see adapter.go's predicateFor and the package doc comment there for the
// exact mapping). client.go is a minimal hand-written HTTP client — no Go
// SDK exists upstream, only a TypeScript one — built against Ghostdive's
// documented REST API (POST/GET /v1/spaces/{spaceID}/memories...).
package ghostdive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a minimal HTTP client for Ghostdive's memory endpoints.
type Client struct {
	baseURL string
	apiKey  string
	httpc   *http.Client
}

// NewClient constructs a Client. baseURL should not have a trailing slash
// (e.g. "http://localhost:8080"). apiKey is sent as a Bearer token.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{baseURL: baseURL, apiKey: apiKey, httpc: &http.Client{}}
}

// Provenance mirrors Ghostdive's memory.Provenance DTO.
type Provenance struct {
	SourceKind     string `json:"source_kind,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
	SourceRecordID string `json:"source_record_id,omitempty"`
	Note           string `json:"note,omitempty"`
}

// Record mirrors the JSON object Ghostdive's recordDTO produces.
type Record struct {
	ID             string     `json:"id"`
	OrgID          string     `json:"org_id"`
	SpaceID        string     `json:"space_id"`
	Layer          string     `json:"layer"`
	PromotionState string     `json:"promotion_state"`
	Subject        string     `json:"subject"`
	Predicate      string     `json:"predicate"`
	Object         string     `json:"object"`
	Summary        string     `json:"summary"`
	Confidence     float64    `json:"confidence"`
	AgentID        string     `json:"agent_id"`
	Provenance     Provenance `json:"provenance"`
	EvidenceRefs   []string   `json:"evidence_refs"`
	Sequence       int64      `json:"sequence"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Transition mirrors Ghostdive's transitionDTO.
type Transition struct {
	ID        string    `json:"id"`
	RecordID  string    `json:"record_id"`
	Sequence  int64     `json:"sequence"`
	FromState string    `json:"from_state"`
	ToState   string    `json:"to_state"`
	Reason    string    `json:"reason"`
	AgentID   string    `json:"agent_id"`
	CreatedAt time.Time `json:"created_at"`
}

// Hit is a Retrieve result: a Record plus its relevance score.
type Hit struct {
	Record
	Score float64 `json:"score"`
}

// ObserveRequest is the body for POST /v1/spaces/{spaceID}/memories.
type ObserveRequest struct {
	Layer             string     `json:"layer"`
	Subject           string     `json:"subject"`
	Predicate         string     `json:"predicate"`
	Object            string     `json:"object"`
	Summary           string     `json:"summary"`
	Confidence        float64    `json:"confidence"`
	AgentID           string     `json:"agent_id"`
	Provenance        Provenance `json:"provenance"`
	EvidenceRefs      []string   `json:"evidence_refs"`
	SkipAutoSupersede bool       `json:"skip_auto_supersede"`
}

// RetrieveRequest is the body for POST /v1/spaces/{spaceID}/memories/retrieve.
type RetrieveRequest struct {
	Query  string   `json:"query"`
	TopK   int      `json:"top_k"`
	Mode   string   `json:"mode"` // lexical | hybrid | dense
	Layers []string `json:"layers"`
	States []string `json:"states"`
}

// TransitionRequest is the body for
// POST /v1/spaces/{spaceID}/memories/{recordID}/transitions.
type TransitionRequest struct {
	ToState string `json:"to_state"`
	Reason  string `json:"reason"`
	AgentID string `json:"agent_id"`
}

// APIError represents a non-2xx JSON error response from Ghostdive.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ghostdive: %s (code=%s status=%d request_id=%s)", e.Message, e.Code, e.StatusCode, e.RequestID)
}

func (c *Client) Observe(ctx context.Context, spaceID string, req ObserveRequest) (Record, error) {
	var rec Record
	path := fmt.Sprintf("/v1/spaces/%s/memories", spaceID)
	if err := c.do(ctx, http.MethodPost, path, req, &rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func (c *Client) Retrieve(ctx context.Context, spaceID string, req RetrieveRequest) ([]Hit, error) {
	var out struct {
		Hits []Hit `json:"hits"`
	}
	path := fmt.Sprintf("/v1/spaces/%s/memories/retrieve", spaceID)
	if err := c.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return nil, err
	}
	return out.Hits, nil
}

func (c *Client) GetRecord(ctx context.Context, spaceID, recordID string) (Record, error) {
	var rec Record
	path := fmt.Sprintf("/v1/spaces/%s/memories/%s", spaceID, recordID)
	if err := c.do(ctx, http.MethodGet, path, nil, &rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func (c *Client) Transition(ctx context.Context, spaceID, recordID string, req TransitionRequest) (Record, Transition, error) {
	var out struct {
		Record     Record     `json:"record"`
		Transition Transition `json:"transition"`
	}
	path := fmt.Sprintf("/v1/spaces/%s/memories/%s/transitions", spaceID, recordID)
	if err := c.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return Record{}, Transition{}, err
	}
	return out.Record, out.Transition, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("ghostdive: marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("ghostdive: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("ghostdive: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var apiErr struct {
			Error     string `json:"error"`
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		return &APIError{StatusCode: resp.StatusCode, Code: apiErr.Code, Message: apiErr.Error, RequestID: apiErr.RequestID}
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("ghostdive: decode response: %w", err)
	}
	return nil
}
