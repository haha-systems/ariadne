package research

import (
	"context"
	"strings"
	"testing"

	"github.com/haha-systems/ariadne/internal/control"
	"github.com/haha-systems/ariadne/internal/invocation"
	"github.com/haha-systems/ariadne/internal/memory/inmemory"
	"github.com/haha-systems/ariadne/internal/scheduler"
)

// TestIntegration_SteerEscalateApproveResume walks the whole interactive
// loop end to end:
//
//	Ari starts -> user steering arrives -> Ari finishes -> steering applied
//	-> Ari escalates -> QAC waits for manual approval -> user approves
//	-> Astra runs -> Astra finishes -> Ari resumes
//
// and then checks that the projection an interface sees and the durable
// event log tell the same story. The two diverging is the failure this
// test exists to catch: a UI showing research history the audit trail does
// not contain would make both untrustworthy.
func TestIntegration_SteerEscalateApproveResume(t *testing.T) {
	eventLog := newEventLog(t)

	// Cycle 0 does routine work; cycle 1 escalates; cycle 2 finishes.
	ari := newGatedRunner(
		invocation.Envelope{
			Status: invocation.StatusContinue, Summary: "ran the delay-matched baseline",
			WorkCompleted: "baseline run", Frontier: "delay alone does not explain the gap",
			Observations: []string{"baseline matches within noise"},
		},
		invocation.Envelope{
			Status:     invocation.StatusEscalate,
			Summary:    "two causal explanations survive",
			Escalation: highValueEscalation("Which of the two surviving explanations is correct?"),
		},
		invocation.Envelope{Status: invocation.StatusComplete, Summary: "programme concluded"},
	)
	astra := &scriptedRunner{envelopes: []invocation.Envelope{{
		Status: invocation.StatusContinue, Summary: "the confound is the shared random seed",
		Frontier: "shared seed identified as the confound",
	}}}

	bus := control.NewBus()
	t.Cleanup(bus.Close)
	store := inmemory.New()

	// The durable JSONL log sits alongside the live bus, exactly as the CLI
	// wires it.
	sink := invocation.MultiSink{eventLog.writer(), bus}

	loop := &AriLoop{
		Ari: ari, Astra: astra, Memory: store, Events: sink,
		AriProfileName: "ari", AstraProfileName: "astra",
	}
	rt := NewRuntime(RuntimeConfig{
		Project: "distributed-cognition", Loop: loop, Memory: store, Bus: bus,
		Logs: control.NewLogBus(200), MaxCycles: 10, QACMode: "manual",
		MemoryBackend: "inmemory",
		EventReplay:   &JSONLReplayer{Path: eventLog.path},
		Agents: []control.AgentSnapshot{
			{Name: AgentAri, Role: "routine"},
			{Name: AgentAstra, Role: "frontier", ScarcityClass: "rare"},
		},
	})
	rt.SetDecider(scheduler.New(scheduler.ControlManual, scheduler.NewBudget(0, 4, 0), rt, "astra"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- the user steers before research has run ---------------------
	const instruction = "Test a monolithic delay-matched baseline before changing the distributed architecture."
	if err := rt.Send(ctx, control.SteerCommand{Message: instruction}); err != nil {
		t.Fatalf("steer: %v", err)
	}

	rt.Start(ctx)
	close(ari.release)

	// --- QAC pauses for a human ---------------------------------------
	var pendingID string
	waitFor(t, "QAC to await manual approval", func() bool {
		snap, _ := rt.Snapshot(ctx)
		if snap.QAC.PendingEscalation == nil {
			return false
		}
		pendingID = snap.QAC.PendingEscalation.ID
		return true
	})

	if astra.callCount() != 0 {
		t.Fatal("Astra ran before the human approved")
	}
	snap, _ := rt.Snapshot(ctx)
	if snap.QAC.Mode != "manual" {
		t.Errorf("QAC mode = %q", snap.QAC.Mode)
	}
	if !snap.QAC.CallsRemainingKnown || snap.QAC.CallsRemaining != 4 {
		t.Errorf("expected the budget to be visible while waiting, got %+v", snap.QAC)
	}

	// --- the user approves --------------------------------------------
	if err := rt.Send(ctx, control.ApproveEscalationCommand{EscalationID: pendingID}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	waitFor(t, "Astra to run once approved", func() bool { return astra.callCount() == 1 })

	<-rt.Done()
	if err := rt.Err(); err != nil {
		t.Fatalf("run ended with error: %v", err)
	}

	// --- the steering instruction reached the first turn and was applied
	prompts := ari.prompts()
	if len(prompts) < 3 {
		t.Fatalf("expected 3 Ari invocations, got %d", len(prompts))
	}
	if !strings.Contains(prompts[0], instruction) {
		t.Error("the steering instruction never reached the first Ari prompt")
	}
	if strings.Contains(prompts[1], "USER STEERING") {
		t.Error("an applied steering instruction must not be re-delivered next cycle")
	}

	steering, err := rt.Steering(ctx)
	if err != nil {
		t.Fatalf("Steering(): %v", err)
	}
	if len(steering) != 1 || steering[0].Status != control.SteeringApplied {
		t.Fatalf("steering records = %+v, want one applied", steering)
	}

	// --- Astra ran exactly once, and control returned to Ari -----------
	if astra.callCount() != 1 {
		t.Errorf("Astra ran %d times, want exactly once", astra.callCount())
	}
	astraPrompt := astra.calls[0].Prompt
	if !strings.Contains(astraPrompt, "You are Astra.") {
		t.Error("Astra's prompt lost its identity assertion")
	}
	if !strings.Contains(astraPrompt, "approved") {
		t.Error("Astra should be told its invocation was approved")
	}
	if !strings.Contains(prompts[2], "shared random seed") {
		t.Error("Astra's intervention did not reach Ari's next prompt")
	}

	// --- the projection and the durable log agree ----------------------
	escalations, err := rt.Escalations(ctx, 0)
	if err != nil {
		t.Fatalf("Escalations(): %v", err)
	}
	if len(escalations) == 0 {
		t.Fatal("no escalation in the projection")
	}
	approved := 0
	for _, e := range escalations {
		if e.Decision == control.DecisionApproved {
			approved++
		}
	}
	if approved != 1 {
		t.Errorf("projection shows %d approved escalations, want 1", approved)
	}

	// Every event the interface could have shown is in the durable log.
	for _, want := range []string{
		invocation.EventResearchCycleStarted,
		invocation.EventAriIdentity,
		invocation.EventAriInvocationStarted,
		invocation.EventAriInvocationCompleted,
		invocation.EventSteeringReceived,
		invocation.EventSteeringApplied,
		invocation.EventEscalationRequested,
		invocation.EventEscalationScored,
		invocation.EventEscalationApproved,
		invocation.EventAstraInvocationStarted,
		invocation.EventAstraInvocationCompleted,
		invocation.EventControlReturnedToAri,
	} {
		if !eventLog.has(want) {
			t.Errorf("durable log is missing %q", want)
		}
	}

	// A fresh interface attaching later recovers the same story from disk.
	replayed, err := rt.RecentEvents(ctx, 500)
	if err != nil {
		t.Fatalf("RecentEvents(): %v", err)
	}
	var sawApproval, sawSteering bool
	for _, ev := range replayed {
		switch ev.Name {
		case invocation.EventEscalationApproved:
			sawApproval = true
		case invocation.EventSteeringApplied:
			sawSteering = true
		}
	}
	if !sawApproval || !sawSteering {
		t.Error("replayed history lost the approval or the applied steering")
	}

	// --- final state -------------------------------------------------
	final, _ := rt.Snapshot(ctx)
	if final.State != control.RunStateStopped {
		t.Errorf("final state = %q, want stopped", final.State)
	}
	if final.Frontier != "shared seed identified as the confound" {
		t.Errorf("final frontier = %q", final.Frontier)
	}
	if final.QAC.PendingEscalation != nil {
		t.Error("a finished run must not still report a pending escalation")
	}
	for _, agent := range final.Agents {
		if agent.State == control.AgentWorking {
			t.Errorf("agent %q still shows as working after the run ended", agent.Name)
		}
	}
}

// TestIntegration_PauseQuitResumePreservesState covers the reopen path:
// pausing, leaving, and coming back must not lose the frontier, the pending
// steering, the quota, or the event history.
func TestIntegration_PauseQuitResumePreservesState(t *testing.T) {
	eventLog := newEventLog(t)
	store := inmemory.New()
	bus := control.NewBus()
	t.Cleanup(bus.Close)

	ari := newGatedRunner(invocation.Envelope{
		Status: invocation.StatusContinue, Summary: "step", Frontier: "frontier after cycle 0",
	})
	loop := &AriLoop{
		Ari: ari, Astra: &scriptedRunner{}, Decider: &scriptedDecider{},
		Memory: store, Events: invocation.MultiSink{eventLog.writer(), bus},
		AriProfileName: "ari", AstraProfileName: "astra",
	}
	rt := NewRuntime(RuntimeConfig{
		Project: "p1", Loop: loop, Memory: store, Bus: bus,
		Logs: control.NewLogBus(100), MaxCycles: 50, QACMode: "automatic",
		EventReplay: &JSONLReplayer{Path: eventLog.path},
		Agents:      []control.AgentSnapshot{{Name: AgentAri}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	rt.Start(ctx)
	close(ari.release)

	// Queue an instruction that has not been delivered yet, then pause.
	waitFor(t, "the first cycle to complete", func() bool {
		snap, _ := rt.Snapshot(ctx)
		return snap.Cycle >= 1
	})
	if err := rt.Send(ctx, control.PauseCommand{}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	waitFor(t, "the loop to pause", func() bool {
		snap, _ := rt.Snapshot(ctx)
		return snap.State == control.RunStatePaused
	})

	if err := rt.Send(ctx, control.SteerCommand{Message: "look at the seed"}); err != nil {
		t.Fatalf("steer: %v", err)
	}

	// Leaving the interface (here, ending the run) must not destroy state.
	cancel()
	<-rt.Done()

	// Reopening: a new runtime over the same store and log.
	bus2 := control.NewBus()
	t.Cleanup(bus2.Close)
	loop2 := &AriLoop{
		Ari: &scriptedRunner{}, Astra: &scriptedRunner{}, Decider: &scriptedDecider{},
		Memory: store, Events: bus2, AriProfileName: "ari", AstraProfileName: "astra",
	}
	rt2 := NewRuntime(RuntimeConfig{
		Project: "p1", Loop: loop2, Memory: store, Bus: bus2,
		Logs: control.NewLogBus(100), QACMode: "automatic",
		EventReplay: &JSONLReplayer{Path: eventLog.path},
		Agents:      []control.AgentSnapshot{{Name: AgentAri}},
	})

	reopened := context.Background()
	snap, err := rt2.Snapshot(reopened)
	if err != nil {
		t.Fatalf("Snapshot() after reopening: %v", err)
	}
	if snap.Frontier != "frontier after cycle 0" {
		t.Errorf("frontier lost across restart: %q", snap.Frontier)
	}

	steering, err := rt2.Steering(reopened)
	if err != nil {
		t.Fatalf("Steering(): %v", err)
	}
	if len(steering) != 1 || steering[0].Status != control.SteeringPending {
		t.Errorf("pending steering lost across restart: %+v", steering)
	}

	events, err := rt2.RecentEvents(reopened, 500)
	if err != nil {
		t.Fatalf("RecentEvents(): %v", err)
	}
	if len(events) == 0 {
		t.Error("event history lost across restart")
	}
	var sawPause bool
	for _, ev := range events {
		if ev.Name == invocation.EventResearchPaused {
			sawPause = true
		}
	}
	if !sawPause {
		t.Error("the pause was never recorded in the durable log")
	}
}
