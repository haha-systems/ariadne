package ghostdive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/haha-systems/ariadne/internal/memory"
)

// ghostdiveClient is the minimal surface Adapter depends on — a local
// interface (per the repo convention of defining interfaces in the
// consuming package) so tests can inject a fake without spinning up an
// httptest server. *Client satisfies it.
type ghostdiveClient interface {
	Observe(ctx context.Context, spaceID string, req ObserveRequest) (Record, error)
	Retrieve(ctx context.Context, spaceID string, req RetrieveRequest) ([]Hit, error)
	GetRecord(ctx context.Context, spaceID, recordID string) (Record, error)
	Transition(ctx context.Context, spaceID, recordID string, req TransitionRequest) (Record, Transition, error)
}

var _ memory.Store = (*Adapter)(nil)

// Adapter implements memory.Store against Ghostdive.
//
// Ghostdive has no native Hypothesis/Decision/Escalation/Frontier concept —
// only Layer/Subject/Predicate/PromotionState. The mapping this file
// maintains:
//
//	Observation  -> layer=episodic,   predicate=observes      state=observed
//	Hypothesis   -> layer=semantic,   predicate=hypothesizes  state=candidate
//	Decision     -> layer=procedural, predicate=decides       state=accepted
//	Escalation   -> layer=allostatic, predicate=escalates     state=observed
//	Steering     -> layer=policy,     predicate=steers        state=observed
//	Frontier     -> layer=policy,     predicate=current-state state=observed
//	               subject "frontier:<project>" (reserved; see WriteFrontier)
//
// Every Ghostdive record is created via Observe, which the server always
// puts in PromotionState "observed" — there is no way to set the initial
// state directly. Kinds whose target state isn't "observed" (Hypothesis,
// Decision) require one additional Transition call right after Observe.
// Every non-Frontier write also sets skip_auto_supersede so append-only
// records can never be silently collapsed by same-subject/predicate
// supersession; Frontier is the one write that intentionally allows it
// (see WriteFrontier).
type Adapter struct {
	client           ghostdiveClient
	spaceIDByProject map[string]string
	// pointerDir stores one file per project recording the Ghostdive record
	// ID currently holding that project's frontier, so Frontier() is an O(1)
	// GetRecord instead of a Retrieve on every call.
	pointerDir string

	mu sync.Mutex // guards pointer file writes for the same project
}

// NewAdapter constructs a Ghostdive-backed memory.Store. pointerDir is
// created on demand.
func NewAdapter(client ghostdiveClient, spaceIDByProject map[string]string, pointerDir string) *Adapter {
	return &Adapter{client: client, spaceIDByProject: spaceIDByProject, pointerDir: pointerDir}
}

var _ memory.Preflighter = (*Adapter)(nil)

// Preflight proves this adapter can actually serve project before an
// expensive research run starts: it checks the project->space mapping, then
// performs one lightweight authenticated read against that space.
//
// The read is what makes this worth doing. A misconfigured or expired
// GHOSTDIVE_API_KEY is invisible until the first request, and the research
// loop's memory writes are deliberately non-fatal — so without a preflight,
// a 401 shows up as a cycle that runs to completion and then silently
// persists nothing, leaving the next cycle to start from no frontier at all.
//
// An empty result is success: a project with no memories yet is normal, and
// only a transport or authorization failure is a preflight failure.
func (a *Adapter) Preflight(ctx context.Context, project string) error {
	spaceID, err := a.spaceID(project)
	if err != nil {
		return fmt.Errorf("ghostdive: preflight: %w", err)
	}

	if _, err := a.client.Retrieve(ctx, spaceID, RetrieveRequest{
		Query: frontierSubject(project),
		TopK:  1,
		Mode:  "lexical",
	}); err != nil {
		return fmt.Errorf("ghostdive: preflight: authenticated read of space %s for project %q failed: %w", spaceID, project, err)
	}
	return nil
}

func (a *Adapter) spaceID(project string) (string, error) {
	id, ok := a.spaceIDByProject[project]
	if !ok {
		return "", fmt.Errorf("ghostdive: no space configured for project %q", project)
	}
	return id, nil
}

// layerAndPredicate returns the Ghostdive layer/predicate convention for a
// Kind that Observe can write directly (Frontier is handled separately by
// WriteFrontier, not through Observe).
func layerAndPredicate(kind memory.Kind) (layer, predicate string, err error) {
	switch kind {
	case memory.KindObservation:
		return "episodic", "observes", nil
	case memory.KindHypothesis:
		return "semantic", "hypothesizes", nil
	case memory.KindDecision:
		return "procedural", "decides", nil
	case memory.KindEscalation:
		return "allostatic", "escalates", nil
	case memory.KindSteering:
		return "policy", "steers", nil
	default:
		return "", "", fmt.Errorf("ghostdive: observe: unsupported kind %q (use WriteFrontier for frontier records)", kind)
	}
}

// targetState is the PromotionState a Kind should end up in after Observe
// (and, if different from Ghostdive's always-"observed" creation state, the
// state Observe transitions it to immediately afterward).
func targetState(kind memory.Kind) string {
	switch kind {
	case memory.KindHypothesis:
		return memory.StateCandidate
	case memory.KindDecision:
		return memory.StateAccepted
	default: // Observation, Escalation
		return memory.StateObserved
	}
}

func kindFromLayerPredicate(layer, predicate string) memory.Kind {
	switch {
	case layer == "episodic" && predicate == "observes":
		return memory.KindObservation
	case layer == "semantic" && predicate == "hypothesizes":
		return memory.KindHypothesis
	case layer == "procedural" && predicate == "decides":
		return memory.KindDecision
	case layer == "allostatic" && predicate == "escalates":
		return memory.KindEscalation
	case layer == "policy" && predicate == "current-state":
		return memory.KindFrontier
	case layer == "policy" && predicate == "steers":
		return memory.KindSteering
	default:
		return memory.Kind(predicate)
	}
}

func toMemoryRecord(rec Record, project string) memory.Record {
	return memory.Record{
		ID:           rec.ID,
		Kind:         kindFromLayerPredicate(rec.Layer, rec.Predicate),
		Project:      project,
		Subject:      rec.Subject,
		Summary:      rec.Summary,
		AgentID:      rec.AgentID,
		State:        rec.PromotionState,
		Confidence:   rec.Confidence,
		EvidenceRefs: rec.EvidenceRefs,
		CreatedAt:    rec.CreatedAt,
	}
}

func (a *Adapter) Observe(ctx context.Context, in memory.ObserveInput) (memory.Record, error) {
	spaceID, err := a.spaceID(in.Project)
	if err != nil {
		return memory.Record{}, err
	}
	layer, predicate, err := layerAndPredicate(in.Kind)
	if err != nil {
		return memory.Record{}, err
	}

	rec, err := a.client.Observe(ctx, spaceID, ObserveRequest{
		Layer:             layer,
		Subject:           in.Subject,
		Predicate:         predicate,
		Summary:           in.Summary,
		Confidence:        in.Confidence,
		AgentID:           in.AgentID,
		EvidenceRefs:      in.EvidenceRefs,
		SkipAutoSupersede: true,
	})
	if err != nil {
		return memory.Record{}, fmt.Errorf("ghostdive: observe: %w", err)
	}

	want := targetState(in.Kind)
	if want != memory.StateObserved {
		rec, _, err = a.client.Transition(ctx, spaceID, rec.ID, TransitionRequest{
			ToState: want,
			Reason:  "initial " + string(in.Kind) + " state",
			AgentID: in.AgentID,
		})
		if err != nil {
			return memory.Record{}, fmt.Errorf("ghostdive: observe: set initial state %q: %w", want, err)
		}
	}

	return toMemoryRecord(rec, in.Project), nil
}

func (a *Adapter) Retrieve(ctx context.Context, in memory.RetrieveInput) ([]memory.Record, error) {
	spaceID, err := a.spaceID(in.Project)
	if err != nil {
		return nil, err
	}

	// Ghostdive's retrieve requires a non-empty query (it's a text search,
	// not a list operation). Callers of memory.Store may leave Query empty
	// to mean "no text filter" (the in-memory backend supports this); against
	// Ghostdive we fall back to the project name as a best-effort broad
	// match. Prefer a non-empty Query for reliable recall against this
	// backend.
	query := in.Query
	if query == "" {
		query = in.Project
	}

	var layers []string
	for _, k := range in.Kinds {
		layer, _, err := layerAndPredicate(k)
		if err != nil {
			continue // Frontier or unknown kinds are never returned by Retrieve
		}
		layers = append(layers, layer)
	}

	hits, err := a.client.Retrieve(ctx, spaceID, RetrieveRequest{
		Query:  query,
		TopK:   in.TopK,
		Mode:   "hybrid",
		Layers: layers,
		States: in.States,
	})
	if err != nil {
		return nil, fmt.Errorf("ghostdive: retrieve: %w", err)
	}

	out := make([]memory.Record, 0, len(hits))
	for _, h := range hits {
		out = append(out, toMemoryRecord(h.Record, in.Project))
	}
	return out, nil
}

// Accept transitions recordID to accepted and writes a new Decision record
// citing it as evidence — mirroring inmemory.Store's design so acceptance
// is itself an auditable fact, not an in-place rewrite.
func (a *Adapter) Accept(ctx context.Context, project, recordID, reason, agentID string) (memory.Record, error) {
	spaceID, err := a.spaceID(project)
	if err != nil {
		return memory.Record{}, err
	}

	rec, _, err := a.client.Transition(ctx, spaceID, recordID, TransitionRequest{
		ToState: memory.StateAccepted,
		Reason:  reason,
		AgentID: agentID,
	})
	if err != nil {
		return memory.Record{}, fmt.Errorf("ghostdive: accept: %w", err)
	}

	if _, err := a.Observe(ctx, memory.ObserveInput{
		Project:      project,
		Subject:      rec.Subject,
		Summary:      reason,
		Kind:         memory.KindDecision,
		AgentID:      agentID,
		EvidenceRefs: []string{recordID},
	}); err != nil {
		return memory.Record{}, fmt.Errorf("ghostdive: accept: record decision: %w", err)
	}

	return toMemoryRecord(rec, project), nil
}

func (a *Adapter) Reject(ctx context.Context, project, recordID, reason, agentID string) (memory.Record, error) {
	spaceID, err := a.spaceID(project)
	if err != nil {
		return memory.Record{}, err
	}
	rec, _, err := a.client.Transition(ctx, spaceID, recordID, TransitionRequest{
		ToState: memory.StateRejected,
		Reason:  reason,
		AgentID: agentID,
	})
	if err != nil {
		return memory.Record{}, fmt.Errorf("ghostdive: reject: %w", err)
	}
	return toMemoryRecord(rec, project), nil
}

// GetEvidence fetches refs in order via a small bounded worker pool (no
// batch-get endpoint exists in Ghostdive), failing fast on the first error
// so a corrupt evidence list never silently produces partial input to a
// scheduling decision.
func (a *Adapter) GetEvidence(ctx context.Context, project string, refs []string) ([]memory.Record, error) {
	spaceID, err := a.spaceID(project)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, nil
	}

	const maxWorkers = 4
	results := make([]memory.Record, len(refs))
	errs := make([]error, len(refs))

	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, ref string) {
			defer wg.Done()
			defer func() { <-sem }()
			rec, err := a.client.GetRecord(ctx, spaceID, ref)
			if err != nil {
				errs[i] = fmt.Errorf("ghostdive: get evidence %s: %w", ref, err)
				return
			}
			results[i] = toMemoryRecord(rec, project)
		}(i, ref)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

// frontierSubject is the reserved subject Frontier records always use, kept
// distinct from every other predicate so Ghostdive's auto-supersede (scoped
// to same subject+predicate) can never touch an Observation, Hypothesis,
// Decision, or Escalation record.
func frontierSubject(project string) string { return "frontier:" + project }

func (a *Adapter) pointerPath(project string) string {
	return filepath.Join(a.pointerDir, project, "frontier_record_id")
}

func (a *Adapter) Frontier(ctx context.Context, project string) (memory.Record, error) {
	spaceID, err := a.spaceID(project)
	if err != nil {
		return memory.Record{}, err
	}

	if data, err := os.ReadFile(a.pointerPath(project)); err == nil {
		recordID := strings.TrimSpace(string(data))
		if rec, err := a.client.GetRecord(ctx, spaceID, recordID); err == nil {
			return toMemoryRecord(rec, project), nil
		}
		// Pointer is stale (e.g. deleted upstream) — self-heal via Retrieve below.
	}

	hits, err := a.client.Retrieve(ctx, spaceID, RetrieveRequest{
		Query:  frontierSubject(project),
		TopK:   1,
		Mode:   "hybrid",
		Layers: []string{"policy"},
	})
	if err != nil {
		return memory.Record{}, fmt.Errorf("ghostdive: frontier: %w", err)
	}
	if len(hits) == 0 {
		return memory.Record{}, fmt.Errorf("ghostdive: no frontier recorded for project %q", project)
	}
	return toMemoryRecord(hits[0].Record, project), nil
}

// WriteFrontier is the one Ghostdive write that intentionally allows
// same-subject/predicate auto-supersede (skip_auto_supersede left false):
// it always targets the reserved frontierSubject(project)/"current-state"
// pair, so repeated writes only ever supersede prior Frontier records, never
// an Observation, Hypothesis, Decision, or Escalation.
func (a *Adapter) WriteFrontier(ctx context.Context, project, summary, agentID string) (memory.Record, error) {
	spaceID, err := a.spaceID(project)
	if err != nil {
		return memory.Record{}, err
	}

	rec, err := a.client.Observe(ctx, spaceID, ObserveRequest{
		Layer:             "policy",
		Subject:           frontierSubject(project),
		Predicate:         "current-state",
		Summary:           summary,
		AgentID:           agentID,
		SkipAutoSupersede: false,
	})
	if err != nil {
		return memory.Record{}, fmt.Errorf("ghostdive: write frontier: %w", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.pointerPath(project)), 0755); err != nil {
		return memory.Record{}, fmt.Errorf("ghostdive: write frontier: pointer dir: %w", err)
	}
	if err := os.WriteFile(a.pointerPath(project), []byte(rec.ID), 0644); err != nil {
		return memory.Record{}, fmt.Errorf("ghostdive: write frontier: pointer file: %w", err)
	}

	return toMemoryRecord(rec, project), nil
}
