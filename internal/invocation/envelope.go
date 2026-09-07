// Package invocation defines the generic "run one agent, get a structured
// result" primitive shared by every workflow (issue-driven and research).
package invocation

import "strings"

// Status is the outcome an agent reports for a single invocation.
type Status string

const (
	// StatusContinue means the agent has more routine work to do.
	StatusContinue Status = "continue"
	// StatusEscalate means the agent wants a scarcer, more capable agent
	// to weigh in before continuing.
	StatusEscalate Status = "escalate"
	// StatusComplete means the agent's goal for this invocation is done.
	StatusComplete Status = "complete"
	// StatusBlocked means the agent cannot proceed without external input.
	StatusBlocked Status = "blocked"
)

// Escalation is the structured request an agent attaches when it returns
// StatusEscalate. It carries enough information for a scheduler to judge
// the epistemic value of the request rather than treating every escalation
// as equally deserving of a scarce agent's time.
//
// Uncertainty, Importance, and ExpectedGain are each in [0,1]; the research
// workflow clamps out-of-range values rather than rejecting the envelope,
// since an agent misreporting a scale should cost it score, not crash the
// cycle.
type Escalation struct {
	// Question is the single question the scarce agent should resolve.
	Question string `json:"question,omitempty"`
	// Reason explains why routine work cannot resolve it.
	Reason string `json:"reason,omitempty"`
	// Uncertainty is how genuinely unresolved the question is.
	Uncertainty float64 `json:"uncertainty,omitempty"`
	// Importance is how much the research programme depends on the answer.
	Importance float64 `json:"importance,omitempty"`
	// ExpectedGain is how much a scarce agent would reduce the uncertainty.
	ExpectedGain float64 `json:"expected_gain,omitempty"`
	// FailedLocalAttempts counts routine attempts already spent on the
	// question — evidence that cheaper avenues are genuinely exhausted.
	FailedLocalAttempts int `json:"failed_local_attempts,omitempty"`
	// CheaperTestAvailable is the agent's own admission that a cheaper
	// discriminating experiment remains, which counts against the request.
	CheaperTestAvailable bool `json:"cheaper_test_available,omitempty"`
	// CandidateExperiment is the experiment the agent would run if it
	// could; it participates in duplicate-request detection.
	CandidateExperiment string `json:"candidate_experiment,omitempty"`
	// EvidenceRefs cite memory records supporting the request.
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

// Envelope is the structured result an agent may write back after a run.
// Agents that don't write one (e.g. today's issue-workflow providers) are
// treated as StatusComplete for backward compatibility.
type Envelope struct {
	Status           Status         `json:"status"`
	Summary          string         `json:"summary"`
	Data             map[string]any `json:"data,omitempty"`
	EscalationReason string         `json:"escalation_reason,omitempty"`
	// Frontier, when non-empty, is an updated compressed summary of the
	// project's current research state. The research workflow persists it
	// via memory.Store.WriteFrontier so the next cycle (and any other
	// researcher reading Ghostdive) sees it — this is how an agent
	// durably records progress rather than it only living in this one
	// invocation's Summary.
	Frontier string `json:"frontier,omitempty"`

	// The fields below carry a research invocation's structured result.
	// They are optional: the issue workflow never sets them, and an agent
	// that reports only status/summary/frontier stays valid.
	Observations       []string `json:"observations,omitempty"`
	Hypotheses         []string `json:"hypotheses,omitempty"`
	RejectedHypotheses []string `json:"rejected_hypotheses,omitempty"`
	OpenQuestions      []string `json:"open_questions,omitempty"`
	WorkCompleted      string   `json:"work_completed,omitempty"`
	NextCandidateStep  string   `json:"next_candidate_step,omitempty"`

	// Escalation carries the structured request behind StatusEscalate.
	// It may be nil even when Status is StatusEscalate — an older agent
	// that only sets EscalationReason still escalates, it just scores as
	// an unevidenced request. See EscalationRequest.
	Escalation *Escalation `json:"escalation,omitempty"`
}

// EscalationRequest normalizes the two ways an agent can request
// escalation: the structured Escalation object, or the original bare
// EscalationReason string. It always returns a non-nil Escalation so
// callers never branch on which vintage of envelope they received.
//
// When only EscalationReason is set, the result carries that text as both
// Question and Reason and leaves every score at zero — an unevidenced
// request, which is exactly how it should be judged.
func (e Envelope) EscalationRequest() Escalation {
	if e.Escalation == nil {
		reason := strings.TrimSpace(e.EscalationReason)
		return Escalation{Question: reason, Reason: reason}
	}

	req := *e.Escalation
	if strings.TrimSpace(req.Question) == "" {
		req.Question = strings.TrimSpace(e.EscalationReason)
	}
	if strings.TrimSpace(req.Reason) == "" {
		req.Reason = strings.TrimSpace(e.EscalationReason)
	}
	return req
}

// EscalationContext is the human-readable explanation of an escalation,
// used as the scheduler's required non-empty Context. It prefers the
// structured question, falling back to the reason and then to the legacy
// EscalationReason field.
func (e Envelope) EscalationContext() string {
	req := e.EscalationRequest()
	for _, candidate := range []string{req.Question, req.Reason, e.EscalationReason} {
		if s := strings.TrimSpace(candidate); s != "" {
			return s
		}
	}
	return ""
}
