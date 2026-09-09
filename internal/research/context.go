package research

import (
	"context"
	"fmt"
	"strings"

	"github.com/haha-systems/ariadne/internal/memory"
)

// Subjects the research workflow writes structured envelope fields under.
// memory.Kind alone is too coarse — observations, open questions, the
// current experiment, and confounds are all episodic observations — so the
// Subject carries the finer distinction, and context recovery reads it back.
const (
	SubjectObservation   = "observation"
	SubjectHypothesis    = "hypothesis"
	SubjectOpenQuestion  = "open_question"
	SubjectExperiment    = "current_experiment"
	SubjectConfound      = "confound"
	SubjectWorkCompleted = "work_completed"
)

// Bounds on how much recovered context is rendered into a prompt. The goal
// is compact durable context, not full memory replay: an Ari prompt should
// re-establish the research state in a few hundred tokens, not re-read the
// whole project history every cycle.
const (
	maxContextObservations = 6
	maxContextHypotheses   = 6
	maxContextRejected     = 6
	maxContextQuestions    = 6
	maxContextItemChars    = 300
	// retrieveTopK is the per-kind fetch ceiling. Records are trimmed to
	// the most recent few afterward; this only bounds the fetch itself.
	retrieveTopK = 50
)

// ResearchContext is the structured state handed to Ari at the start of a
// cycle, reconstructed from durable memory rather than from a single
// frontier summary. Recovering hypotheses, rejections, and open questions
// separately is what stops each cycle re-deriving context the previous
// cycle already established.
type ResearchContext struct {
	Frontier           string
	Observations       []string
	ActiveHypotheses   []string
	RejectedHypotheses []string
	OpenQuestions      []string
	CurrentExperiment  string
	LastAstraDecision  string
	KnownConfounds     []string
	// FrontierMissing reports that no frontier has ever been recorded,
	// which is normal on a project's first cycle and worth stating
	// explicitly rather than rendering as an empty section.
	FrontierMissing bool
}

// LoadContext reconstructs a project's research context from store. Partial
// failure is tolerated on purpose: a retrieval error for one section leaves
// that section empty rather than failing the cycle, since a degraded prompt
// is still far better than no research step at all. A missing frontier is
// likewise not an error.
func LoadContext(ctx context.Context, store memory.Store, project string) ResearchContext {
	rc := ResearchContext{}

	if frontier, err := store.Frontier(ctx, project); err == nil && strings.TrimSpace(frontier.Summary) != "" {
		rc.Frontier = frontier.Summary
	} else {
		rc.FrontierMissing = true
	}

	observations := retrieve(ctx, store, project, []memory.Kind{memory.KindObservation}, nil)
	rc.Observations = lastSummaries(bySubject(observations, SubjectObservation), maxContextObservations)
	rc.OpenQuestions = lastSummaries(bySubject(observations, SubjectOpenQuestion), maxContextQuestions)
	rc.KnownConfounds = lastSummaries(bySubject(observations, SubjectConfound), maxContextObservations)

	if experiments := bySubject(observations, SubjectExperiment); len(experiments) > 0 {
		rc.CurrentExperiment = truncate(experiments[len(experiments)-1].Summary, maxContextItemChars)
	}

	hypotheses := retrieve(ctx, store, project, []memory.Kind{memory.KindHypothesis}, nil)
	rc.ActiveHypotheses = lastSummaries(byState(hypotheses, memory.StateCandidate, memory.StateAccepted), maxContextHypotheses)
	rc.RejectedHypotheses = lastSummaries(byState(hypotheses, memory.StateRejected, memory.StateDeprecated), maxContextRejected)

	decisions := retrieve(ctx, store, project, []memory.Kind{memory.KindDecision}, nil)
	if astra := byAgent(decisions, "astra"); len(astra) > 0 {
		rc.LastAstraDecision = truncate(astra[len(astra)-1].Summary, maxContextItemChars)
	}

	return rc
}

// Render formats the context as the prompt's RESEARCH CONTEXT body. Empty
// sections are omitted entirely so the prompt never carries dead headers.
func (rc ResearchContext) Render() string {
	var b strings.Builder

	if rc.Frontier != "" {
		fmt.Fprintf(&b, "CURRENT FRONTIER\n\n%s\n\n", truncate(rc.Frontier, maxFrontierChars))
	} else if rc.FrontierMissing {
		b.WriteString("CURRENT FRONTIER\n\nNo frontier recorded yet — this may be the project's first cycle.\n\n")
	}

	writeList(&b, "LATEST OBSERVATIONS", rc.Observations)
	writeList(&b, "ACTIVE HYPOTHESES", rc.ActiveHypotheses)
	writeList(&b, "REJECTED HYPOTHESES", rc.RejectedHypotheses)
	writeList(&b, "OPEN QUESTIONS", rc.OpenQuestions)

	if rc.CurrentExperiment != "" {
		fmt.Fprintf(&b, "CURRENT EXPERIMENT\n\n%s\n\n", rc.CurrentExperiment)
	}
	if rc.LastAstraDecision != "" {
		fmt.Fprintf(&b, "LAST ASTRA DECISION\n\n%s\n\n", rc.LastAstraDecision)
	}
	writeList(&b, "KNOWN CONFOUNDS", rc.KnownConfounds)

	return strings.TrimSpace(b.String())
}

func writeList(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "%s\n\n", title)
	for _, item := range items {
		fmt.Fprintf(b, "- %s\n", item)
	}
	b.WriteString("\n")
}

// retrieve is a best-effort fetch: an error yields no records rather than
// failing the caller, matching LoadContext's degrade-don't-fail contract.
func retrieve(ctx context.Context, store memory.Store, project string, kinds []memory.Kind, states []string) []memory.Record {
	recs, err := store.Retrieve(ctx, memory.RetrieveInput{
		Project: project,
		Kinds:   kinds,
		States:  states,
		TopK:    retrieveTopK,
	})
	if err != nil {
		return nil
	}
	return recs
}

func bySubject(recs []memory.Record, subject string) []memory.Record {
	var out []memory.Record
	for _, rec := range recs {
		if rec.Subject == subject {
			out = append(out, rec)
		}
	}
	return out
}

func byState(recs []memory.Record, states ...string) []memory.Record {
	var out []memory.Record
	for _, rec := range recs {
		for _, want := range states {
			if rec.State == want {
				out = append(out, rec)
				break
			}
		}
	}
	return out
}

func byAgent(recs []memory.Record, agentID string) []memory.Record {
	var out []memory.Record
	for _, rec := range recs {
		if rec.AgentID == agentID {
			out = append(out, rec)
		}
	}
	return out
}

// lastSummaries takes the most recent n records' summaries. Store.Retrieve
// promises no ordering beyond "most relevant first", so this is a
// best-effort recency heuristic over whatever order the backend returned —
// the in-memory backend appends chronologically, which makes the tail the
// newest records.
func lastSummaries(recs []memory.Record, n int) []string {
	if len(recs) > n {
		recs = recs[len(recs)-n:]
	}
	out := make([]string, 0, len(recs))
	for _, rec := range recs {
		if s := strings.TrimSpace(rec.Summary); s != "" {
			out = append(out, truncate(s, maxContextItemChars))
		}
	}
	return out
}
