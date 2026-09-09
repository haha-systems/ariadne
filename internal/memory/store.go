// Package memory defines the storage-agnostic interface for shared
// cognitive memory. Workflows and the scheduler depend only on Store; the
// in-memory backend (package inmemory) is the default, and Ghostdive
// (package ghostdive) is an optional, pluggable alternative — nothing in
// this package or its callers requires Ghostdive to exist.
package memory

import (
	"context"
	"time"
)

// Kind distinguishes the semantic role of a Record. It intentionally mirrors
// the vocabulary the research workflow (and the spec) uses, independent of
// how any particular backend represents it internally.
type Kind string

const (
	KindObservation Kind = "observation"
	KindHypothesis  Kind = "hypothesis"
	KindDecision    Kind = "decision"
	KindEscalation  Kind = "escalation"
	// KindSteering is a user instruction to the research programme. It is
	// stored in shared memory rather than in an interface's own state so
	// an instruction survives a UI restart and stays part of the auditable
	// research record — see control.SteeringRecord.
	KindSteering Kind = "steering"
	// KindFrontier marks the single mutable per-project summary record
	// returned by Frontier/WriteFrontier. It is never produced by Observe.
	KindFrontier Kind = "frontier"
)

// Lifecycle states a Record can occupy. Named to match Ghostdive's
// PromotionState vocabulary so the in-memory and Ghostdive backends agree on
// meaning, even though Ghostdive is not required.
const (
	StateObserved   = "observed"
	StateCandidate  = "candidate"
	StateAccepted   = "accepted"
	StateRejected   = "rejected"
	StateDeprecated = "deprecated"
)

// Record is one unit of shared memory: an observation, a hypothesis, a
// decision, an escalation, or (exactly one per project) the frontier
// summary.
type Record struct {
	ID           string
	Kind         Kind
	Project      string
	Subject      string
	Summary      string
	AgentID      string
	State        string
	Confidence   float64
	EvidenceRefs []string
	CreatedAt    time.Time
}

// ObserveInput describes a new append-only Record to write.
type ObserveInput struct {
	Project      string
	Subject      string
	Summary      string
	Kind         Kind
	Confidence   float64
	AgentID      string
	EvidenceRefs []string
}

// RetrieveInput filters a project's memory. Query is a best-effort text
// match; backends that support real search (e.g. Ghostdive) may use it for
// ranking, but callers must not assume ordering beyond "most relevant
// first."
type RetrieveInput struct {
	Project string
	Query   string
	Kinds   []Kind
	States  []string
	TopK    int
}

// Store is the shared cognitive memory every workflow depends on.
//
// Observations are append-only: once written they are never mutated.
// Hypotheses and Escalations move through Accept/Reject, which record a
// state transition rather than rewriting the original record — Accept also
// produces a new Decision Record that cites the accepted record as
// evidence, so acceptance is itself an auditable fact rather than an
// in-place edit. Frontier is the one intentionally mutable convenience
// representation: WriteFrontier may be called repeatedly for the same
// project, and it must never affect the append-only records above.
type Store interface {
	Observe(ctx context.Context, in ObserveInput) (Record, error)
	Retrieve(ctx context.Context, in RetrieveInput) ([]Record, error)
	// Accept and Reject are scoped to project: backends that partition
	// storage by project (e.g. Ghostdive's per-project space) need this to
	// know where to look, and it prevents a record ID from one project from
	// accidentally resolving against another.
	Accept(ctx context.Context, project, recordID, reason, agentID string) (Record, error)
	Reject(ctx context.Context, project, recordID, reason, agentID string) (Record, error)
	// GetEvidence fetches refs, scoped to project, for the same reason.
	GetEvidence(ctx context.Context, project string, refs []string) ([]Record, error)
	Frontier(ctx context.Context, project string) (Record, error)
	WriteFrontier(ctx context.Context, project, summary, agentID string) (Record, error)
}

// Preflighter is an optional Store capability: a cheap, authenticated
// round-trip that proves the backend is actually usable for a project
// before an expensive run begins.
//
// It exists because a backend can satisfy the Store interface and still be
// unusable at runtime — a missing or rejected API key surfaces only on the
// first real write, by which point an agent has already done a cycle's
// worth of work whose results are then silently lost. Preflight moves that
// failure to before the first invocation.
//
// A Preflight error means "this backend cannot serve this project": bad
// credentials, an unmapped project, an unreachable host. It must not fail
// merely because the project has no data yet.
type Preflighter interface {
	Preflight(ctx context.Context, project string) error
}

// Preflight validates store for project when the backend supports it.
// Backends that don't implement Preflighter (the in-memory store, test
// fakes) are always considered ready, so callers can invoke this
// unconditionally.
func Preflight(ctx context.Context, store Store, project string) error {
	p, ok := store.(Preflighter)
	if !ok {
		return nil
	}
	return p.Preflight(ctx, project)
}
