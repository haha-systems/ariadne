// Package inmemory is the default memory.Store backend: a mutex-guarded map
// with no external dependency, used whenever Ghostdive isn't configured and
// throughout the test suite.
package inmemory

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/haha-systems/ariadne/internal/memory"
)

var _ memory.Store = (*Store)(nil)

// Store implements memory.Store in-process.
type Store struct {
	mu        sync.Mutex
	records   map[string]memory.Record
	order     []string // insertion order, for deterministic Retrieve results
	frontiers map[string]memory.Record
	nextID    int
	now       func() time.Time // overridable in tests
}

// New returns an empty Store.
func New() *Store {
	return &Store{
		records:   make(map[string]memory.Record),
		frontiers: make(map[string]memory.Record),
		now:       time.Now,
	}
}

func (s *Store) newID(prefix string) string {
	s.nextID++
	return prefix + "-" + strconv.Itoa(s.nextID)
}

// initialState picks the lifecycle state a freshly Observe'd Record starts
// in, per Kind. See memory.Store's doc comment for the append-only /
// transition semantics this must preserve.
func initialState(k memory.Kind) string {
	switch k {
	case memory.KindHypothesis:
		return memory.StateCandidate
	case memory.KindDecision:
		return memory.StateAccepted
	default: // Observation, Escalation
		return memory.StateObserved
	}
}

func (s *Store) Observe(_ context.Context, in memory.ObserveInput) (memory.Record, error) {
	if in.Project == "" {
		return memory.Record{}, fmt.Errorf("inmemory: observe: project is required")
	}
	if in.Kind == memory.KindFrontier {
		return memory.Record{}, fmt.Errorf("inmemory: observe: use WriteFrontier for frontier records")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec := memory.Record{
		ID:           s.newID(string(in.Kind)),
		Kind:         in.Kind,
		Project:      in.Project,
		Subject:      in.Subject,
		Summary:      in.Summary,
		AgentID:      in.AgentID,
		State:        initialState(in.Kind),
		Confidence:   in.Confidence,
		EvidenceRefs: append([]string(nil), in.EvidenceRefs...),
		CreatedAt:    s.now(),
	}
	s.records[rec.ID] = rec
	s.order = append(s.order, rec.ID)
	return rec, nil
}

func (s *Store) Retrieve(_ context.Context, in memory.RetrieveInput) ([]memory.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []memory.Record
	for _, id := range s.order {
		rec := s.records[id]
		if rec.Project != in.Project {
			continue
		}
		if len(in.Kinds) > 0 && !containsKind(in.Kinds, rec.Kind) {
			continue
		}
		if len(in.States) > 0 && !containsString(in.States, rec.State) {
			continue
		}
		if in.Query != "" && !matchesQuery(rec, in.Query) {
			continue
		}
		out = append(out, rec)
		if in.TopK > 0 && len(out) >= in.TopK {
			break
		}
	}
	return out, nil
}

func matchesQuery(rec memory.Record, query string) bool {
	haystack := strings.ToLower(rec.Subject + " " + rec.Summary)
	return strings.Contains(haystack, strings.ToLower(query))
}

func containsKind(kinds []memory.Kind, k memory.Kind) bool {
	for _, want := range kinds {
		if want == k {
			return true
		}
	}
	return false
}

func containsString(vals []string, v string) bool {
	for _, want := range vals {
		if want == v {
			return true
		}
	}
	return false
}

// Accept transitions recordID to StateAccepted and writes a new Decision
// Record citing it as evidence, mirroring the Ghostdive adapter's two-write
// design: the acceptance itself becomes a separately queryable, auditable
// fact rather than an in-place rewrite of the original record.
func (s *Store) Accept(_ context.Context, project, recordID, reason, agentID string) (memory.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[recordID]
	if !ok || rec.Project != project {
		return memory.Record{}, fmt.Errorf("inmemory: accept: record %q not found in project %q", recordID, project)
	}
	rec.State = memory.StateAccepted
	s.records[recordID] = rec

	decision := memory.Record{
		ID:           s.newID("decision"),
		Kind:         memory.KindDecision,
		Project:      rec.Project,
		Subject:      rec.Subject,
		Summary:      reason,
		AgentID:      agentID,
		State:        memory.StateAccepted,
		EvidenceRefs: []string{recordID},
		CreatedAt:    s.now(),
	}
	s.records[decision.ID] = decision
	s.order = append(s.order, decision.ID)

	return rec, nil
}

func (s *Store) Reject(_ context.Context, project, recordID, reason, agentID string) (memory.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[recordID]
	if !ok || rec.Project != project {
		return memory.Record{}, fmt.Errorf("inmemory: reject: record %q not found in project %q", recordID, project)
	}
	rec.State = memory.StateRejected
	s.records[recordID] = rec
	_ = agentID // recorded implicitly via the record's own AgentID; reason is not stored separately in-memory
	return rec, nil
}

// GetEvidence fetches refs in order, failing fast on the first missing
// record (or one that belongs to a different project) so a corrupt evidence
// list never silently produces partial input to a scheduling decision.
func (s *Store) GetEvidence(_ context.Context, project string, refs []string) ([]memory.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]memory.Record, 0, len(refs))
	for _, ref := range refs {
		rec, ok := s.records[ref]
		if !ok || rec.Project != project {
			return nil, fmt.Errorf("inmemory: get evidence %q: not found in project %q", ref, project)
		}
		out = append(out, rec)
	}
	return out, nil
}

// Frontier and WriteFrontier operate on a map kept entirely separate from
// records/order, so a frontier write can never overwrite or shadow an
// append-only Observation/Hypothesis/Decision/Escalation.
func (s *Store) Frontier(_ context.Context, project string) (memory.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.frontiers[project]
	if !ok {
		return memory.Record{}, fmt.Errorf("inmemory: no frontier recorded for project %q", project)
	}
	return rec, nil
}

func (s *Store) WriteFrontier(_ context.Context, project, summary, agentID string) (memory.Record, error) {
	if project == "" {
		return memory.Record{}, fmt.Errorf("inmemory: write frontier: project is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec := memory.Record{
		ID:        s.newID("frontier"),
		Kind:      memory.KindFrontier,
		Project:   project,
		Subject:   "frontier:" + project,
		Summary:   summary,
		AgentID:   agentID,
		State:     memory.StateAccepted,
		CreatedAt: s.now(),
	}
	s.frontiers[project] = rec
	return rec, nil
}
