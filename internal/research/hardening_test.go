package research

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haha-systems/ariadne/internal/invocation"
	"github.com/haha-systems/ariadne/internal/memory"
	"github.com/haha-systems/ariadne/internal/memory/inmemory"
	"github.com/haha-systems/ariadne/internal/research/prompts"
	"github.com/haha-systems/ariadne/internal/scheduler"
)

// The tests in this file are regressions for the failure modes observed in
// the first live research pass: Ari adopting Astra's identity from project
// prose, Ari running an unbounded research programme inside one invocation,
// QAC never getting control, and a 401 from the memory backend silently
// discarding every cycle's results.

// eventLog captures the loop's event stream to a temp file and reads it
// back as parsed JSON lines, so tests can assert on the audit trail the
// workflow actually emits rather than on internal state.
type eventLog struct {
	t    *testing.T
	path string
	file *os.File
}

func newEventLog(t *testing.T) *eventLog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create event log: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return &eventLog{t: t, path: path, file: f}
}

func (e *eventLog) writer() *invocation.EventWriter {
	return invocation.NewEventWriter(e.file, "test")
}

func (e *eventLog) events() []map[string]any {
	e.t.Helper()
	f, err := os.Open(e.path)
	if err != nil {
		e.t.Fatalf("open event log: %v", err)
	}
	defer f.Close()

	var out []map[string]any
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func (e *eventLog) find(name string) []map[string]any {
	var out []map[string]any
	for _, ev := range e.events() {
		if ev["event"] == name {
			out = append(out, ev)
		}
	}
	return out
}

func (e *eventLog) has(name string) bool { return len(e.find(name)) > 0 }

// failingStore fails a chosen operation, standing in for a Ghostdive
// backend that authenticates on some calls and rejects others.
type failingStore struct {
	memory.Store
	preflightErr error
	frontierWrit error
}

func (s *failingStore) Preflight(_ context.Context, _ string) error { return s.preflightErr }

func (s *failingStore) WriteFrontier(ctx context.Context, project, summary, agentID string) (memory.Record, error) {
	if s.frontierWrit != nil {
		return memory.Record{}, s.frontierWrit
	}
	return s.Store.WriteFrontier(ctx, project, summary, agentID)
}

// --- Identity ---------------------------------------------------------

// TestAriPrompt_GuidanceNamingAstraAsLeadDoesNotChangeAriIdentity is the
// direct regression for the observed leak: project guidance described Astra
// as the lead researcher, and the Ari invocation continued as Astra.
func TestAriPrompt_GuidanceNamingAstraAsLeadDoesNotChangeAriIdentity(t *testing.T) {
	prompt := prompts.BuildAri(prompts.AriInput{
		Project:  "p1",
		Guidance: "Research team roster:\n- Astra is the lead researcher.\n- Ari assists.",
	})

	for _, want := range []string{
		"You are Ari.",
		"You are not Astra.",
		"only be invoked by Ariadne after an approved QAC escalation",
		"You cannot promote yourself to another role.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("Ari prompt missing identity assertion %q", want)
		}
	}

	// The identity must be asserted before the guidance can claim
	// otherwise, and re-asserted after it.
	identityAt := strings.Index(prompt, "You are Ari.")
	guidanceAt := strings.Index(prompt, "Astra is the lead researcher.")
	if identityAt < 0 || guidanceAt < 0 || identityAt > guidanceAt {
		t.Errorf("expected identity assertion before guidance prose (identity=%d guidance=%d)", identityAt, guidanceAt)
	}
	if !strings.Contains(prompt[guidanceAt:], "You are Ari, the routine research agent. You are not Astra.") {
		t.Error("expected identity to be re-asserted after the guidance block")
	}
	if !strings.Contains(prompt, "reference material") {
		t.Error("expected guidance to be fenced as reference material, not as instructions")
	}
}

func TestAstraPrompt_AssertsAstraIdentityAndForbidsRecursion(t *testing.T) {
	prompt := prompts.BuildAstra(prompts.AstraInput{
		Project:  "p1",
		Question: "q",
		Guidance: "Ari is the routine agent and runs the programme day to day.",
	})

	for _, want := range []string{
		"You are Astra.",
		"handling exactly one approved escalation",
		"must not escalate to Astra",
		"control returns to Ari",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("Astra prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, `"escalate"`) && !strings.Contains(prompt, `Do not return status "escalate"`) {
		t.Error("Astra's envelope contract must not offer escalate as an option")
	}
}

func TestAriLoop_LogsAriIdentityForEveryInvocation(t *testing.T) {
	log := newEventLog(t)
	ari := &scriptedRunner{envelopes: []invocation.Envelope{{Status: invocation.StatusComplete}}}
	loop := newTestLoop(ari, &scriptedRunner{}, &scriptedDecider{}, inmemory.New())
	loop.Events = log.writer()

	if err := loop.Run(context.Background(), "p1", 1); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	identity := log.find(invocation.EventAriIdentity)
	if len(identity) != 1 {
		t.Fatalf("expected 1 ari_identity event, got %d", len(identity))
	}
	if identity[0]["agent"] != AgentAri || identity[0]["not"] != AgentAstra {
		t.Errorf("ari_identity event = %+v, want agent=ari not=astra", identity[0])
	}
}

// --- Bounded turns ----------------------------------------------------

func TestAriPrompt_RequestsOneBoundedStepAndNoContinuousWork(t *testing.T) {
	prompt := prompts.BuildAri(prompts.AriInput{Project: "p1"})

	for _, want := range []string{
		"Perform exactly one bounded research step.",
		"smallest action that materially reduces uncertainty",
		"Do not attempt to complete the whole research programme",
		"return control to Ariadne with a structured status",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("Ari prompt missing bounded-turn instruction %q", want)
		}
	}

	// The old prompt's continuous-work framing is what kept control away
	// from QAC; it must not survive anywhere in a routine prompt.
	for _, banned := range []string{
		"work continuously",
		"do not stop at a plan",
		"keep experimenting until",
		"Continue routine research work",
	} {
		if strings.Contains(strings.ToLower(prompt), strings.ToLower(banned)) {
			t.Errorf("Ari prompt still contains unbounded-work instruction %q", banned)
		}
	}
}

func TestAriLoop_ContinueStatus_ReturnsControlAfterOneStepPerCycle(t *testing.T) {
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusContinue, Summary: "step 1", WorkCompleted: "inspected result A"},
		{Status: invocation.StatusContinue, Summary: "step 2", WorkCompleted: "ran experiment B"},
		{Status: invocation.StatusComplete, Summary: "done"},
	}}
	loop := newTestLoop(ari, &scriptedRunner{}, &scriptedDecider{}, inmemory.New())

	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// Three cycles, three invocations: one bounded step each, control
	// returning to the loop between them.
	if len(ari.calls) != 3 {
		t.Errorf("expected 3 Ari invocations (one bounded step per cycle), got %d", len(ari.calls))
	}
}

// --- QAC participation, approval, denial ------------------------------

func TestAriLoop_EscalationRequest_ProducesSchedulerDecisionEvent(t *testing.T) {
	log := newEventLog(t)
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, Escalation: highValueEscalation("which mechanism explains E14?")},
		{Status: invocation.StatusComplete},
	}}
	decider := &scriptedDecider{decision: scheduler.Decision{Escalated: true, Reason: "high value"}}

	loop := newTestLoop(ari, &scriptedRunner{}, decider, inmemory.New())
	loop.Events = log.writer()
	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(decider.calls) == 0 {
		t.Fatal("expected the escalation request to reach the scheduler")
	}
	for _, want := range []string{
		invocation.EventEscalationRequested,
		invocation.EventEscalationScored,
		invocation.EventEscalationApproved,
		invocation.EventControlReturnedToAri,
	} {
		if !log.has(want) {
			t.Errorf("expected event %q to be recorded", want)
		}
	}

	scored := log.find(invocation.EventEscalationScored)[0]
	if score, ok := scored["score"].(float64); !ok || score <= continueWeight {
		t.Errorf("expected a high-value request to score above the continue baseline, got %v", scored["score"])
	}
}

func TestAriLoop_HighValueEscalation_InvokesAstraExactlyOnce(t *testing.T) {
	realSched := scheduler.New(scheduler.ControlAutomatic, scheduler.NewBudget(0, 0, 0), nil, "astra")
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, Escalation: highValueEscalation("which mechanism explains E14?")},
		{Status: invocation.StatusComplete},
	}}
	astra := &scriptedRunner{envelopes: []invocation.Envelope{{Status: invocation.StatusContinue, Summary: "run the crossed design"}}}

	loop := newTestLoop(ari, astra, realSched, inmemory.New())
	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(astra.calls) != 1 {
		t.Fatalf("expected exactly 1 Astra invocation, got %d", len(astra.calls))
	}
	if astra.calls[0].Depth != 1 {
		t.Errorf("expected Astra to be invoked at Depth 1, got %d", astra.calls[0].Depth)
	}
}

func TestAriLoop_LowValueEscalation_DeniedAndFeedbackReachesAri(t *testing.T) {
	realSched := scheduler.New(scheduler.ControlAutomatic, scheduler.NewBudget(0, 0, 0), nil, "astra")
	lowValue := &invocation.Escalation{
		Question:             "should I look at this more?",
		Reason:               "not sure",
		Uncertainty:          0.2,
		Importance:           0.2,
		ExpectedGain:         0.1,
		CheaperTestAvailable: true,
	}
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, Escalation: lowValue},
		{Status: invocation.StatusComplete},
	}}
	astra := &scriptedRunner{}

	loop := newTestLoop(ari, astra, realSched, inmemory.New())
	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(astra.calls) != 0 {
		t.Errorf("expected a low-value escalation to be denied, got %d Astra calls", len(astra.calls))
	}
	if len(ari.calls) < 2 {
		t.Fatalf("expected Ari to be invoked again after the denial, got %d calls", len(ari.calls))
	}

	next := ari.calls[1].Prompt
	if !strings.Contains(next, "previous escalation request was denied") {
		t.Error("expected the next Ari prompt to carry the denial")
	}
	if !strings.Contains(next, "cheaper discriminating experiment remains available") {
		t.Errorf("expected an actionable recommended constraint, got:\n%s", next)
	}
	// Feedback is delivered exactly once, not repeated forever.
	if len(ari.calls) > 2 && strings.Contains(ari.calls[2].Prompt, "previous escalation request was denied") {
		t.Error("denial feedback should be delivered once, not repeated every cycle")
	}
}

// TestScoreEscalation_DifferentEvidenceProducesDifferentDecisions is the
// acceptance criterion for replacing the static 0.4/0.6 weights: under one
// unchanged budget, the evidence alone must be able to flip the outcome.
func TestScoreEscalation_DifferentEvidenceProducesDifferentDecisions(t *testing.T) {
	strong := invocation.Escalation{Uncertainty: 0.9, Importance: 0.9, ExpectedGain: 0.8, FailedLocalAttempts: 3}
	weak := invocation.Escalation{Uncertainty: 0.2, Importance: 0.2, ExpectedGain: 0.1, CheaperTestAvailable: true}

	strongScore := ScoreEscalation(strong, false)
	weakScore := ScoreEscalation(weak, false)

	if !(strongScore.Total > continueWeight) {
		t.Errorf("strong request scored %.2f, expected above the %.2f baseline", strongScore.Total, continueWeight)
	}
	if !(weakScore.Total < continueWeight) {
		t.Errorf("weak request scored %.2f, expected below the %.2f baseline", weakScore.Total, continueWeight)
	}

	// Same budget, same scheduler, opposite outcomes.
	for _, tc := range []struct {
		name       string
		score      float64
		wantEscala bool
	}{
		{"strong", strongScore.Total, true},
		{"weak", weakScore.Total, false},
	} {
		sched := scheduler.New(scheduler.ControlAutomatic, scheduler.NewBudget(0, 0, 0), nil, "astra")
		decision, err := sched.Decide(context.Background(), scheduler.DecisionRequest{
			Context: "why",
			Options: []scheduler.Action{
				{Name: "continue", ProfileName: "ari", ScarcityClass: "plentiful", Weight: continueWeight},
				{Name: "escalate", ProfileName: "astra", ScarcityClass: "rare", Weight: tc.score},
			},
		})
		if err != nil {
			t.Fatalf("%s: Decide() error = %v", tc.name, err)
		}
		if decision.Escalated != tc.wantEscala {
			t.Errorf("%s: Escalated = %v, want %v (score %.2f)", tc.name, decision.Escalated, tc.wantEscala, tc.score)
		}
	}
}

func TestScoreEscalation_ClampsOutOfRangeSelfReports(t *testing.T) {
	inflated := invocation.Escalation{Uncertainty: 99, Importance: 99, ExpectedGain: 99, FailedLocalAttempts: 500}
	if got := ScoreEscalation(inflated, false).Total; got != 1 {
		t.Errorf("Total = %v, want the score clamped to 1", got)
	}
	negative := invocation.Escalation{Uncertainty: -5, Importance: -5, ExpectedGain: -5}
	if got := ScoreEscalation(negative, false).Total; got != 0 {
		t.Errorf("Total = %v, want the score clamped to 0", got)
	}
}

// --- Duplicate escalation protection ----------------------------------

func TestAriLoop_RepeatedEscalationWithoutNewEvidence_IsRejected(t *testing.T) {
	log := newEventLog(t)
	realSched := scheduler.New(scheduler.ControlAutomatic, scheduler.NewBudget(0, 0, 0), nil, "astra")

	lowValue := &invocation.Escalation{Question: "same question", Reason: "same reason", Uncertainty: 0.2, Importance: 0.2}
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, Escalation: lowValue},
		{Status: invocation.StatusEscalate, Escalation: lowValue}, // identical resend
		{Status: invocation.StatusComplete},
	}}

	loop := newTestLoop(ari, &scriptedRunner{}, realSched, inmemory.New())
	loop.Events = log.writer()
	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	scored := log.find(invocation.EventEscalationScored)
	if len(scored) < 2 {
		t.Fatalf("expected both requests to be scored, got %d", len(scored))
	}
	if dup, _ := scored[1]["duplicate_of_denied"].(bool); !dup {
		t.Error("expected the identical resend to be flagged as a duplicate of a denied request")
	}
	if first, second := scored[0]["score"].(float64), scored[1]["score"].(float64); second >= first {
		t.Errorf("expected the duplicate to be penalized (first=%.2f second=%.2f)", first, second)
	}

	next := ari.calls[2].Prompt
	if !strings.Contains(next, "materially identical to one already denied") {
		t.Errorf("expected duplicate-specific feedback, got:\n%s", next)
	}
}

func TestEscalationFingerprint_NewEvidenceIsNotADuplicate(t *testing.T) {
	base := invocation.Escalation{Question: "which mechanism?", Reason: "unclear", EvidenceRefs: []string{"ev-1"}}
	reworded := invocation.Escalation{Question: "  WHICH   Mechanism? ", Reason: "Unclear", EvidenceRefs: []string{"ev-1"}}
	withNewEvidence := invocation.Escalation{Question: "which mechanism?", Reason: "unclear", EvidenceRefs: []string{"ev-1", "ev-2"}}

	if escalationFingerprint(base) != escalationFingerprint(reworded) {
		t.Error("expected trivial rewording to fingerprint identically")
	}
	if escalationFingerprint(base) == escalationFingerprint(withNewEvidence) {
		t.Error("expected new evidence to produce a different fingerprint, so the request is reconsidered")
	}
}

// --- Memory preflight and persistence ---------------------------------

// TestAriLoop_PreflightFailure_StopsBeforeAnyInvocation is the regression
// for the 401: an unusable memory backend must cost nothing, not a full
// research pass whose results are then silently discarded.
func TestAriLoop_PreflightFailure_StopsBeforeAnyInvocation(t *testing.T) {
	log := newEventLog(t)
	ari := &scriptedRunner{envelopes: []invocation.Envelope{{Status: invocation.StatusComplete}}}
	store := &failingStore{Store: inmemory.New(), preflightErr: errors.New("401 unauthorized")}

	loop := newTestLoop(ari, &scriptedRunner{}, &scriptedDecider{}, store)
	loop.Events = log.writer()

	err := loop.Run(context.Background(), "p1", 5)
	if err == nil {
		t.Fatal("expected Run() to fail when the memory backend fails preflight")
	}
	if !strings.Contains(err.Error(), "401 unauthorized") {
		t.Errorf("expected the underlying cause to be wrapped, got %v", err)
	}
	if len(ari.calls) != 0 {
		t.Errorf("expected no Ari invocation after a failed preflight, got %d", len(ari.calls))
	}
	if !log.has(invocation.EventMemoryPreflightFailed) {
		t.Error("expected a memory_preflight_failed event")
	}
}

func TestAriLoop_PreflightPasses_RunsNormally(t *testing.T) {
	log := newEventLog(t)
	ari := &scriptedRunner{envelopes: []invocation.Envelope{{Status: invocation.StatusComplete}}}
	store := &failingStore{Store: inmemory.New()}

	loop := newTestLoop(ari, &scriptedRunner{}, &scriptedDecider{}, store)
	loop.Events = log.writer()
	if err := loop.Run(context.Background(), "p1", 1); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !log.has(invocation.EventMemoryPreflightPassed) {
		t.Error("expected a memory_preflight_passed event")
	}
	if len(ari.calls) != 1 {
		t.Errorf("expected 1 Ari invocation, got %d", len(ari.calls))
	}
}

// TestAriLoop_StrictPersistence_WriteFailureStopsTheRun covers §10: a
// failed write of research state must never be treated as though the state
// had been saved.
func TestAriLoop_StrictPersistence_WriteFailureStopsTheRun(t *testing.T) {
	log := newEventLog(t)
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusContinue, Summary: "step", Frontier: "new frontier"},
	}}
	store := &failingStore{Store: inmemory.New(), frontierWrit: errors.New("401 unauthorized")}

	loop := newTestLoop(ari, &scriptedRunner{}, &scriptedDecider{}, store)
	loop.Events = log.writer()
	loop.StrictPersistence = true

	err := loop.Run(context.Background(), "p1", 5)
	if err == nil {
		t.Fatal("expected a frontier write failure to stop a strict-persistence run")
	}
	if !log.has(invocation.EventMemoryWriteFailed) {
		t.Error("expected a memory_write_failed event")
	}
	if len(ari.calls) != 1 {
		t.Errorf("expected the run to stop after the failed write, got %d Ari invocations", len(ari.calls))
	}
}

func TestAriLoop_NonStrictPersistence_WriteFailureIsLoggedAndTolerated(t *testing.T) {
	log := newEventLog(t)
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusContinue, Summary: "step", Frontier: "new frontier"},
		{Status: invocation.StatusComplete},
	}}
	store := &failingStore{Store: inmemory.New(), frontierWrit: errors.New("transient")}

	loop := newTestLoop(ari, &scriptedRunner{}, &scriptedDecider{}, store)
	loop.Events = log.writer()

	if err := loop.Run(context.Background(), "p1", 5); err != nil {
		t.Fatalf("Run() error = %v, want the failure tolerated when not strict", err)
	}
	if !log.has(invocation.EventMemoryWriteFailed) {
		t.Error("expected the failure to still be recorded, never silently swallowed")
	}
}

// --- Frontier and structured context recovery -------------------------

// TestAriLoop_StructuredStateWrittenInCycleN_VisibleInCycleNPlus1 is the
// continuity regression: what a cycle establishes must reach the next
// cycle's prompt, rather than being re-derived from scratch.
func TestAriLoop_StructuredStateWrittenInCycleN_VisibleInCycleNPlus1(t *testing.T) {
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{
			Status:             invocation.StatusContinue,
			Summary:            "cycle 0 summary",
			Frontier:           "frontier after cycle 0",
			Observations:       []string{"E14 shows a one-bit advantage"},
			Hypotheses:         []string{"nonlinearity explains the advantage"},
			RejectedHypotheses: []string{"sampling noise explains the advantage"},
			OpenQuestions:      []string{"does the advantage survive a crossed design?"},
			NextCandidateStep:  "run the crossed design",
		},
		{Status: invocation.StatusComplete},
	}}

	loop := newTestLoop(ari, &scriptedRunner{}, &scriptedDecider{}, inmemory.New())
	if err := loop.Run(context.Background(), "p1", 5); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(ari.calls) < 2 {
		t.Fatalf("expected a second cycle, got %d Ari invocations", len(ari.calls))
	}

	second := ari.calls[1].Prompt
	for _, want := range []string{
		"CURRENT FRONTIER",
		"frontier after cycle 0",
		"LATEST OBSERVATIONS",
		"E14 shows a one-bit advantage",
		"ACTIVE HYPOTHESES",
		"nonlinearity explains the advantage",
		"REJECTED HYPOTHESES",
		"sampling noise explains the advantage",
		"OPEN QUESTIONS",
		"does the advantage survive a crossed design?",
		"CURRENT EXPERIMENT",
		"run the crossed design",
	} {
		if !strings.Contains(second, want) {
			t.Errorf("cycle 1 prompt missing recovered context %q", want)
		}
	}
}

func TestAriLoop_AstraDecision_SurfacesInAriContext(t *testing.T) {
	realSched := scheduler.New(scheduler.ControlAutomatic, scheduler.NewBudget(0, 0, 0), nil, "astra")
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, Escalation: highValueEscalation("which mechanism?")},
		{Status: invocation.StatusComplete},
	}}
	astra := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusContinue, Summary: "the confound is the shared random seed"},
	}}

	loop := newTestLoop(ari, astra, realSched, inmemory.New())
	if err := loop.Run(context.Background(), "p1", 5); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(ari.calls) < 2 {
		t.Fatalf("expected control to return to Ari, got %d invocations", len(ari.calls))
	}
	next := ari.calls[1].Prompt
	if !strings.Contains(next, "LAST ASTRA DECISION") || !strings.Contains(next, "the confound is the shared random seed") {
		t.Errorf("expected Astra's intervention to reach Ari's next prompt, got:\n%s", next)
	}
}

func TestLoadContext_NoMemoryYet_ReportsFirstCycle(t *testing.T) {
	rc := LoadContext(context.Background(), inmemory.New(), "fresh")
	if !rc.FrontierMissing {
		t.Error("expected FrontierMissing on a project with no memory")
	}
	rendered := rc.Render()
	if !strings.Contains(rendered, "No frontier recorded yet") {
		t.Errorf("Render() = %q, want an explicit first-cycle note", rendered)
	}
	for _, absent := range []string{"LATEST OBSERVATIONS", "ACTIVE HYPOTHESES", "OPEN QUESTIONS"} {
		if strings.Contains(rendered, absent) {
			t.Errorf("expected empty section %q to be omitted entirely", absent)
		}
	}
}

// --- Envelope compatibility -------------------------------------------

func TestEnvelope_LegacyEscalationReason_StillEscalatesAsUnevidenced(t *testing.T) {
	env := invocation.Envelope{Status: invocation.StatusEscalate, EscalationReason: "need astra"}

	req := env.EscalationRequest()
	if req.Question != "need astra" || req.Reason != "need astra" {
		t.Errorf("EscalationRequest() = %+v, want the legacy reason carried into both fields", req)
	}
	if env.EscalationContext() != "need astra" {
		t.Errorf("EscalationContext() = %q", env.EscalationContext())
	}
	// An unevidenced request must lose to routine work.
	if score := ScoreEscalation(req, false); score.Total >= continueWeight {
		t.Errorf("unevidenced legacy escalation scored %.2f, expected below the %.2f baseline", score.Total, continueWeight)
	}
}
