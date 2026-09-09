package research

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/haha-systems/ariadne/internal/invocation"
)

// Escalation scoring turns an escalation request into the Weight the
// generic scheduler compares against the "continue" option. The scheduler
// stays workflow-agnostic — it only knows scarcity, budget, and control
// mode — so the epistemic judgement lives here, in the research workflow
// that actually understands what uncertainty and expected gain mean.
//
// The previous implementation used a fixed continue=0.4 / escalate=0.6
// pair, which meant the act of requesting an escalation decided the
// outcome. These weights instead make a request compete: an unevidenced or
// low-value request scores below continueWeight and loses.
const (
	// continueWeight is the fixed weight of "keep doing routine work".
	// An escalation must beat it on epistemic value alone.
	continueWeight = 0.5

	// The three epistemic factors sum to 1.0, so a maximally-scored
	// request with no pressure or penalties lands exactly at 1.0.
	weightUncertainty  = 0.35
	weightImportance   = 0.35
	weightExpectedGain = 0.30

	// maxFailurePressure is the bonus for having genuinely exhausted
	// routine avenues, reached at failureAttemptsCap attempts.
	maxFailurePressure = 0.20
	failureAttemptsCap = 3

	// cheaperTestPenalty applies when the agent itself reports that a
	// cheaper discriminating experiment remains available. Running that
	// experiment is strictly better than spending scarce quota.
	cheaperTestPenalty = 0.40

	// duplicatePenalty applies to a request materially identical to one
	// already denied, with no new evidence attached.
	duplicatePenalty = 0.50
)

// EscalationScore is the inspectable breakdown of one scoring decision.
// The components are kept alongside the total so the escalation_scored
// event explains *why* a request scored what it did, rather than emitting
// an unexplained float.
type EscalationScore struct {
	Total           float64
	Uncertainty     float64
	Importance      float64
	ExpectedGain    float64
	FailurePressure float64
	CheaperTest     float64
	Duplicate       float64
	// Duplicated reports whether this request was recognized as a repeat
	// of an already-denied one.
	Duplicated bool
}

// Fields renders the score as event-log fields.
func (s EscalationScore) Fields() map[string]any {
	return map[string]any{
		"score":                   s.Total,
		"score_uncertainty":       s.Uncertainty,
		"score_importance":        s.Importance,
		"score_expected_gain":     s.ExpectedGain,
		"score_failure_pressure":  s.FailurePressure,
		"score_cheaper_test":      s.CheaperTest,
		"score_duplicate_penalty": s.Duplicate,
		"duplicate_of_denied":     s.Duplicated,
	}
}

// ScoreEscalation computes the escalate option's Weight from the request's
// own evidence. duplicated marks a request already denied once with no new
// evidence. The result is clamped to [0,1].
func ScoreEscalation(req invocation.Escalation, duplicated bool) EscalationScore {
	score := EscalationScore{
		Uncertainty:  weightUncertainty * clamp01(req.Uncertainty),
		Importance:   weightImportance * clamp01(req.Importance),
		ExpectedGain: weightExpectedGain * clamp01(req.ExpectedGain),
		Duplicated:   duplicated,
	}

	attempts := req.FailedLocalAttempts
	if attempts > failureAttemptsCap {
		attempts = failureAttemptsCap
	}
	if attempts > 0 {
		score.FailurePressure = maxFailurePressure * float64(attempts) / failureAttemptsCap
	}

	if req.CheaperTestAvailable {
		score.CheaperTest = cheaperTestPenalty
	}
	if duplicated {
		score.Duplicate = duplicatePenalty
	}

	score.Total = clamp01(
		score.Uncertainty + score.Importance + score.ExpectedGain +
			score.FailurePressure - score.CheaperTest - score.Duplicate,
	)
	return score
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// escalationFingerprint identifies a request for duplicate detection. It
// covers the question, the candidate experiment, and the cited evidence —
// so attaching genuinely new evidence produces a different fingerprint and
// the request is no longer treated as a repeat, which is exactly the escape
// hatch the duplicate rule needs.
func escalationFingerprint(req invocation.Escalation) string {
	refs := append([]string(nil), req.EvidenceRefs...)
	sort.Strings(refs)

	parts := []string{
		normalizeForFingerprint(req.Question),
		normalizeForFingerprint(req.Reason),
		normalizeForFingerprint(req.CandidateExperiment),
		normalizeForFingerprint(strings.Join(refs, ",")),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// normalizeForFingerprint collapses whitespace and case so trivial
// rewording of the same request still fingerprints identically.
func normalizeForFingerprint(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// denialFeedback renders the message Ari receives on its next cycle after a
// denied escalation. Without it, Ari has no way to learn why the request
// failed and simply re-sends it, burning cycles on a request that cannot
// succeed.
func denialFeedback(req invocation.Escalation, reason string, score EscalationScore) string {
	var b strings.Builder

	b.WriteString("Your previous escalation request was denied.\n\n")
	if q := strings.TrimSpace(req.Question); q != "" {
		fmt.Fprintf(&b, "Request: %s\n\n", q)
	}

	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "the scheduler did not approve the request"
	}
	fmt.Fprintf(&b, "Reason:\n%s\n\n", reason)
	fmt.Fprintf(&b, "Epistemic value scored %.2f against a continue baseline of %.2f.\n\n", score.Total, continueWeight)

	b.WriteString("Recommended constraint:\n")
	switch {
	case score.Duplicated:
		b.WriteString("This request was materially identical to one already denied. Do not re-send it. Either gather new evidence that changes the picture, or pursue a different line of work.")
	case req.CheaperTestAvailable:
		b.WriteString("A cheaper discriminating experiment remains available. Run that experiment before requesting escalation again.")
	case req.FailedLocalAttempts == 0:
		b.WriteString("You have not yet attempted this question locally. Make at least one routine attempt, and report what it ruled out, before escalating.")
	default:
		b.WriteString("Continue routine work on this question and escalate again only with stronger evidence that it is genuinely unresolvable locally — or with a more honest assessment of its uncertainty, importance, and expected gain.")
	}
	return b.String()
}
