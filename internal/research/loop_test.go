package research

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/haha-systems/ariadne/internal/invocation"
	"github.com/haha-systems/ariadne/internal/memory"
	"github.com/haha-systems/ariadne/internal/memory/inmemory"
	"github.com/haha-systems/ariadne/internal/scheduler"
)

// scriptedRunner is a hand-written fake invocation.Runner that returns a
// scripted sequence of envelopes, one per call; the last envelope repeats
// once the script is exhausted.
// It is mutex-guarded because the runtime tests drive the loop on a
// background goroutine while the test goroutine polls what it has done.
type scriptedRunner struct {
	mu        sync.Mutex
	envelopes []invocation.Envelope
	calls     []invocation.Request
	err       error
}

// callCount is the concurrency-safe way to observe progress from a test
// goroutine while the loop runs.
func (r *scriptedRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *scriptedRunner) Invoke(_ context.Context, req invocation.Request) (invocation.Envelope, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, req)
	if r.err != nil {
		return invocation.Envelope{}, r.err
	}
	if len(r.envelopes) == 0 {
		return invocation.Envelope{Status: invocation.StatusComplete}, nil
	}
	idx := len(r.calls) - 1
	if idx >= len(r.envelopes) {
		idx = len(r.envelopes) - 1
	}
	return r.envelopes[idx], nil
}

// scriptedDecider is a hand-written fake Decider.
type scriptedDecider struct {
	decision scheduler.Decision
	err      error
	calls    []scheduler.DecisionRequest
}

func (d *scriptedDecider) Decide(_ context.Context, req scheduler.DecisionRequest) (scheduler.Decision, error) {
	d.calls = append(d.calls, req)
	if d.err != nil {
		return scheduler.Decision{}, d.err
	}
	return d.decision, nil
}

// highValueEscalation is a request whose evidence clears the continue
// baseline, for tests that need the scheduler to actually approve.
func highValueEscalation(question string) *invocation.Escalation {
	return &invocation.Escalation{
		Question:            question,
		Reason:              "routine work has exhausted the cheap discriminators",
		Uncertainty:         0.9,
		Importance:          0.9,
		ExpectedGain:        0.8,
		FailedLocalAttempts: 3,
	}
}

func newTestLoop(ari, astra *scriptedRunner, decider Decider, store memory.Store) *AriLoop {
	return &AriLoop{
		Ari: ari, Astra: astra, Decider: decider, Memory: store,
		AriProfileName: "ari", AstraProfileName: "astra",
	}
}

func TestAriLoop_Prompt_IncludesScopeNoticeGuidanceProtocolAndFrontier(t *testing.T) {
	ari := &scriptedRunner{envelopes: []invocation.Envelope{{Status: invocation.StatusComplete}}}
	astra := &scriptedRunner{}
	store := inmemory.New()
	if _, err := store.WriteFrontier(context.Background(), "p1", "parity holds under nonlinearity", "ari"); err != nil {
		t.Fatalf("WriteFrontier() error = %v", err)
	}

	loop := newTestLoop(ari, astra, &scriptedDecider{}, store)
	loop.Guidance = "Team mandate: characterize the parity effect."
	if err := loop.Run(context.Background(), "p1", 1); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(ari.calls) != 1 {
		t.Fatalf("expected 1 Ari invocation, got %d", len(ari.calls))
	}
	prompt := ari.calls[0].Prompt
	for _, want := range []string{"SCOPE:", "Team mandate: characterize the parity effect.", "$ARIADNE_OUTPUT_FILE", "parity holds under nonlinearity"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("expected prompt to contain %q, got %q", want, prompt)
		}
	}
}

func TestAriLoop_EnvelopeFrontier_PersistedAsNewFrontier(t *testing.T) {
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusComplete, Summary: "wrapped up this cycle", Frontier: "updated: nonlinearity confirmed via E14"},
	}}
	astra := &scriptedRunner{}
	store := inmemory.New()

	loop := newTestLoop(ari, astra, &scriptedDecider{}, store)
	if err := loop.Run(context.Background(), "p1", 1); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	frontier, err := store.Frontier(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Frontier() error = %v", err)
	}
	if frontier.Summary != "updated: nonlinearity confirmed via E14" {
		t.Errorf("Frontier().Summary = %q, want the envelope's Frontier field to have been persisted", frontier.Summary)
	}
}

func TestAriLoop_EnvelopeWithoutFrontier_LeavesExistingFrontierUntouched(t *testing.T) {
	ari := &scriptedRunner{envelopes: []invocation.Envelope{{Status: invocation.StatusComplete, Summary: "no update this cycle"}}}
	astra := &scriptedRunner{}
	store := inmemory.New()
	if _, err := store.WriteFrontier(context.Background(), "p1", "original frontier", "ari"); err != nil {
		t.Fatalf("WriteFrontier() error = %v", err)
	}

	loop := newTestLoop(ari, astra, &scriptedDecider{}, store)
	if err := loop.Run(context.Background(), "p1", 1); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	frontier, err := store.Frontier(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Frontier() error = %v", err)
	}
	if frontier.Summary != "original frontier" {
		t.Errorf("Frontier().Summary = %q, want it unchanged since the envelope had no Frontier field", frontier.Summary)
	}
}

func TestAriLoop_AstraEnvelopeFrontier_AlsoPersisted(t *testing.T) {
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, EscalationReason: "need astra"},
		{Status: invocation.StatusComplete},
	}}
	astra := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusComplete, Summary: "resolved the fork", Frontier: "astra's updated frontier"},
	}}
	decider := &scriptedDecider{decision: scheduler.Decision{Escalated: true, Reason: "high value fork"}}
	store := inmemory.New()

	loop := newTestLoop(ari, astra, decider, store)
	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	frontier, err := store.Frontier(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Frontier() error = %v", err)
	}
	if frontier.Summary != "astra's updated frontier" {
		t.Errorf("Frontier().Summary = %q, want Astra's Frontier field to have been persisted too", frontier.Summary)
	}
}

func TestAriLoop_NormalContinue_NeverInvokesAstra(t *testing.T) {
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusContinue, Summary: "inspecting results"},
		{Status: invocation.StatusContinue, Summary: "implemented experiment"},
		{Status: invocation.StatusComplete, Summary: "done"},
	}}
	astra := &scriptedRunner{}
	store := inmemory.New()

	loop := newTestLoop(ari, astra, &scriptedDecider{}, store)
	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(ari.calls) != 3 {
		t.Errorf("expected 3 Ari invocations (stopping at StatusComplete), got %d", len(ari.calls))
	}
	if len(astra.calls) != 0 {
		t.Errorf("expected Astra to never be invoked, got %d calls", len(astra.calls))
	}

	obs, _ := store.Retrieve(context.Background(), memory.RetrieveInput{Project: "p1", Kinds: []memory.Kind{memory.KindObservation}})
	if len(obs) != 3 {
		t.Errorf("expected each Ari summary persisted as an observation, got %d", len(obs))
	}
}

func TestAriLoop_ApprovedEscalation_InvokesAstraWithPacket(t *testing.T) {
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, Summary: "two hypotheses survive", EscalationReason: "cannot distinguish A from B"},
		{Status: invocation.StatusComplete, Summary: "implemented Astra's suggested experiment"},
	}}
	astra := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusComplete, Summary: "run experiment E14: vary X while holding Y constant"},
	}}
	decider := &scriptedDecider{decision: scheduler.Decision{
		Chosen: scheduler.Action{Name: "escalate", ProfileName: "astra"}, Escalated: true, Reason: "high expected value fork",
	}}
	store := inmemory.New()
	store.WriteFrontier(context.Background(), "p1", "current frontier summary", "ari") //nolint:errcheck

	loop := newTestLoop(ari, astra, decider, store)
	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(astra.calls) != 1 {
		t.Fatalf("expected exactly 1 Astra invocation, got %d", len(astra.calls))
	}
	if astra.calls[0].Depth != 1 {
		t.Errorf("expected Astra invoked at Depth 1, got %d", astra.calls[0].Depth)
	}
	packetText := astra.calls[0].Prompt
	if !strings.Contains(packetText, "cannot distinguish A from B") {
		t.Errorf("expected packet to include the escalation reason, got %q", packetText)
	}
	if !strings.Contains(packetText, "current frontier summary") {
		t.Errorf("expected packet to include the frontier, got %q", packetText)
	}

	decisions, _ := store.Retrieve(context.Background(), memory.RetrieveInput{Project: "p1", Kinds: []memory.Kind{memory.KindDecision}})
	found := false
	for _, d := range decisions {
		if strings.Contains(d.Summary, "experiment E14") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected Astra's output persisted as a Decision record, got %+v", decisions)
	}

	if len(ari.calls) != 2 {
		t.Errorf("expected control to return to Ari after Astra, got %d Ari calls", len(ari.calls))
	}
}

func TestAriLoop_ApprovedEscalation_PersistsAcceptedEscalationRecord(t *testing.T) {
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, EscalationReason: "two hypotheses survive"},
		{Status: invocation.StatusComplete},
	}}
	astra := &scriptedRunner{envelopes: []invocation.Envelope{{Status: invocation.StatusComplete, Summary: "run E14"}}}
	decider := &scriptedDecider{decision: scheduler.Decision{Escalated: true, Reason: "high value fork"}}
	store := inmemory.New()

	loop := newTestLoop(ari, astra, decider, store)
	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	escalations, _ := store.Retrieve(context.Background(), memory.RetrieveInput{Project: "p1", Kinds: []memory.Kind{memory.KindEscalation}})
	if len(escalations) != 1 {
		t.Fatalf("expected 1 escalation record, got %d", len(escalations))
	}
	if escalations[0].State != memory.StateAccepted {
		t.Errorf("escalation State = %q, want accepted", escalations[0].State)
	}
}

func TestAriLoop_DeniedEscalation_PersistsRejectedEscalationRecord(t *testing.T) {
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, EscalationReason: "not that important yet"},
		{Status: invocation.StatusComplete},
	}}
	astra := &scriptedRunner{}
	decider := &scriptedDecider{decision: scheduler.Decision{Escalated: false, Reason: "expected gain too low"}}
	store := inmemory.New()

	loop := newTestLoop(ari, astra, decider, store)
	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	escalations, _ := store.Retrieve(context.Background(), memory.RetrieveInput{Project: "p1", Kinds: []memory.Kind{memory.KindEscalation}})
	if len(escalations) != 1 {
		t.Fatalf("expected 1 escalation record, got %d", len(escalations))
	}
	if escalations[0].State != memory.StateRejected {
		t.Errorf("escalation State = %q, want rejected", escalations[0].State)
	}
}

func TestAriLoop_DeniedEscalation_NeverInvokesAstra(t *testing.T) {
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, EscalationReason: "not that important yet"},
		{Status: invocation.StatusComplete},
	}}
	astra := &scriptedRunner{}
	decider := &scriptedDecider{decision: scheduler.Decision{
		Chosen: scheduler.Action{Name: "continue", ProfileName: "ari"}, Escalated: false, Reason: "expected gain did not exceed scarcity threshold",
	}}
	store := inmemory.New()

	loop := newTestLoop(ari, astra, decider, store)
	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(astra.calls) != 0 {
		t.Errorf("expected Astra to never be invoked on denial, got %d calls", len(astra.calls))
	}
	if len(ari.calls) != 2 {
		t.Errorf("expected Ari to continue after denial, got %d calls", len(ari.calls))
	}
}

func TestAriLoop_BudgetExhaustion_TerminatesViaRealScheduler(t *testing.T) {
	// Use a real scheduler under ControlDisabled instead of a scripted
	// Decider, to verify the loop composes correctly with QAC's own
	// denial path (not just a mocked Decision).
	realSched := scheduler.New(scheduler.ControlDisabled, scheduler.NewBudget(0, 0, 0), nil, "astra")

	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, EscalationReason: "need astra"},
		{Status: invocation.StatusComplete},
	}}
	astra := &scriptedRunner{}
	store := inmemory.New()

	loop := newTestLoop(ari, astra, realSched, store)
	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(astra.calls) != 0 {
		t.Errorf("expected ControlDisabled to prevent any Astra invocation, got %d calls", len(astra.calls))
	}
}

// TestAriLoop_AstraFurtherEscalation_RejectedByRecursionGuard is a direct
// regression test for the "no Astra self-recursion" guardrail: if Astra's
// own envelope requests further escalation, the loop must not invoke Astra
// again — the scheduler's recursion guard must reject it, and control must
// still return to Ari.
func TestAriLoop_AstraFurtherEscalation_RejectedByRecursionGuard(t *testing.T) {
	// A genuinely high-value request, so the real scheduler approves it on
	// epistemic value and Astra actually runs — this test is about what
	// happens *after* approval, not about the scoring gate.
	ari := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, Escalation: highValueEscalation("need astra")},
		{Status: invocation.StatusComplete},
	}}
	astra := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusEscalate, Summary: "even I am unsure", EscalationReason: "need an even stronger model"},
	}}
	// A real scheduler (not a scripted Decider) so the actual
	// guardAgainstRecursion logic runs.
	realSched := scheduler.New(scheduler.ControlAutomatic, scheduler.NewBudget(0, 0, 0), nil, "astra")
	store := inmemory.New()

	loop := newTestLoop(ari, astra, realSched, store)
	if err := loop.Run(context.Background(), "p1", 10); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(astra.calls) != 1 {
		t.Errorf("expected exactly 1 Astra invocation despite it requesting further escalation, got %d", len(astra.calls))
	}
	if len(ari.calls) != 2 {
		t.Errorf("expected control to return to Ari after the rejected recursive escalation, got %d Ari calls", len(ari.calls))
	}
}

func TestAriLoop_AriInvocationError_StopsTheLoop(t *testing.T) {
	ari := &scriptedRunner{err: errors.New("provider crashed")}
	astra := &scriptedRunner{}
	store := inmemory.New()

	loop := newTestLoop(ari, astra, &scriptedDecider{}, store)
	err := loop.Run(context.Background(), "p1", 10)
	if err == nil {
		t.Fatal("expected Run() to return an error when Ari's invocation fails")
	}
}
