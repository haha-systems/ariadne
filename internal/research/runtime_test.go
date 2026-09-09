package research

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haha-systems/ariadne/internal/control"
	"github.com/haha-systems/ariadne/internal/invocation"
	"github.com/haha-systems/ariadne/internal/memory/inmemory"
	"github.com/haha-systems/ariadne/internal/scheduler"
)

// gatedRunner blocks each invocation until the test releases it, so a test
// can observe the runtime mid-invocation rather than only at the end.
type gatedRunner struct {
	mu        sync.Mutex
	release   chan struct{}
	started   chan string
	envelopes []invocation.Envelope
	calls     []invocation.Request
}

func newGatedRunner(envelopes ...invocation.Envelope) *gatedRunner {
	return &gatedRunner{
		release:   make(chan struct{}),
		started:   make(chan string, 16),
		envelopes: envelopes,
	}
}

func (g *gatedRunner) Invoke(ctx context.Context, req invocation.Request) (invocation.Envelope, error) {
	g.mu.Lock()
	idx := len(g.calls)
	g.calls = append(g.calls, req)
	g.mu.Unlock()

	select {
	case g.started <- req.Prompt:
	default:
	}

	select {
	case <-g.release:
	case <-ctx.Done():
		return invocation.Envelope{}, ctx.Err()
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.envelopes) == 0 {
		return invocation.Envelope{Status: invocation.StatusComplete}, nil
	}
	if idx >= len(g.envelopes) {
		idx = len(g.envelopes) - 1
	}
	return g.envelopes[idx], nil
}

func (g *gatedRunner) prompts() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.calls))
	for _, c := range g.calls {
		out = append(out, c.Prompt)
	}
	return out
}

// newTestRuntime builds a Runtime over a scripted loop and an in-memory
// store, wired the same way cmd/ariadne wires the real one.
func newTestRuntime(t *testing.T, ari, astra invocation.Runner, decider Decider) (*Runtime, *control.Bus) {
	t.Helper()

	bus := control.NewBus()
	t.Cleanup(bus.Close)

	loop := &AriLoop{
		Ari: ari, Astra: astra, Decider: decider,
		Memory:           inmemory.New(),
		Events:           bus,
		AriProfileName:   "ari",
		AstraProfileName: "astra",
	}
	rt := NewRuntime(RuntimeConfig{
		Project: "p1", Loop: loop, Memory: loop.Memory, Bus: bus,
		Logs: control.NewLogBus(100), MaxCycles: 5, QACMode: "automatic",
		MemoryBackend: "inmemory",
		Agents: []control.AgentSnapshot{
			{Name: AgentAri, Role: "routine", Model: "m-ari", Provider: "codex"},
			{Name: AgentAstra, Role: "frontier", Model: "m-astra", Provider: "claude"},
		},
	})
	return rt, bus
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- Commands ---------------------------------------------------------

func TestControl_ValidateRejectsMalformedCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  control.ResearchCommand
	}{
		{"empty steer", control.SteerCommand{}},
		{"whitespace steer", control.SteerCommand{Message: "   "}},
		{"approve without id", control.ApproveEscalationCommand{}},
		{"deny without id", control.DenyEscalationCommand{Reason: "no"}},
	} {
		if err := control.Validate(tc.cmd); !errors.Is(err, control.ErrInvalidCommand) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidCommand", tc.name, err)
		}
	}

	for _, cmd := range []control.ResearchCommand{
		control.SteerCommand{Message: "do the thing"},
		control.PauseCommand{},
		control.ResumeCommand{},
		control.ApproveEscalationCommand{EscalationID: "e1"},
	} {
		if err := control.Validate(cmd); err != nil {
			t.Errorf("Validate(%T) = %v, want nil", cmd, err)
		}
	}
}

func TestRuntime_Send_InvalidCommandRejectedWithoutTouchingResearch(t *testing.T) {
	rt, _ := newTestRuntime(t, &scriptedRunner{}, &scriptedRunner{}, &scriptedDecider{})

	err := rt.Send(context.Background(), control.SteerCommand{Message: "  "})
	if !errors.Is(err, control.ErrInvalidCommand) {
		t.Fatalf("Send() = %v, want ErrInvalidCommand", err)
	}
	records, err := rt.Steering(context.Background())
	if err != nil {
		t.Fatalf("Steering() error = %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected no steering record for a rejected command, got %d", len(records))
	}
}

// --- Steering ---------------------------------------------------------

func TestRuntime_Steering_PersistsPendingAndReachesTheNextAriPrompt(t *testing.T) {
	ari := newGatedRunner(
		invocation.Envelope{Status: invocation.StatusContinue, Summary: "cycle 0"},
		invocation.Envelope{Status: invocation.StatusComplete, Summary: "done"},
	)
	rt, _ := newTestRuntime(t, ari, &scriptedRunner{}, &scriptedDecider{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const instruction = "Test a monolithic delay-matched baseline before changing the architecture."
	if err := rt.Send(ctx, control.SteerCommand{Message: instruction}); err != nil {
		t.Fatalf("Send(steer) error = %v", err)
	}

	records, err := rt.Steering(ctx)
	if err != nil {
		t.Fatalf("Steering() error = %v", err)
	}
	if len(records) != 1 || records[0].Status != control.SteeringPending {
		t.Fatalf("steering records = %+v, want one pending", records)
	}
	if records[0].Message != instruction {
		t.Errorf("Message = %q", records[0].Message)
	}

	rt.Start(ctx)
	close(ari.release)
	<-rt.Done()

	prompts := ari.prompts()
	if len(prompts) == 0 {
		t.Fatal("expected at least one Ari invocation")
	}
	if !strings.Contains(prompts[0], "USER STEERING") || !strings.Contains(prompts[0], instruction) {
		t.Errorf("expected the instruction in the next Ari prompt, got:\n%s", prompts[0])
	}

	records, err = rt.Steering(ctx)
	if err != nil {
		t.Fatalf("Steering() error = %v", err)
	}
	if records[0].Status != control.SteeringApplied {
		t.Errorf("Status = %q, want applied once it reached an agent", records[0].Status)
	}
}

// Steering adjusts priorities; it must never read as licence to rewrite the
// evidence record.
func TestRuntime_SteeringPrompt_ForbidsRewritingTheRecord(t *testing.T) {
	ari := newGatedRunner(invocation.Envelope{Status: invocation.StatusComplete})
	rt, _ := newTestRuntime(t, ari, &scriptedRunner{}, &scriptedDecider{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := rt.Send(ctx, control.SteerCommand{Message: "try X first"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	rt.Start(ctx)
	close(ari.release)
	<-rt.Done()

	prompt := ari.prompts()[0]
	for _, want := range []string{
		"do not use them to revise or discard existing observations",
		"rejected hypotheses",
		"They do not overwrite the project mandate",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("steering block missing guard %q", want)
		}
	}
}

// --- Pause and resume -------------------------------------------------

func TestRuntime_PauseStopsAtSafeBoundaryAndResumeContinues(t *testing.T) {
	ari := newGatedRunner(invocation.Envelope{Status: invocation.StatusContinue, Summary: "step"})
	rt, _ := newTestRuntime(t, ari, &scriptedRunner{}, &scriptedDecider{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rt.Start(ctx)

	// Let the first invocation begin, then ask to pause while it runs.
	select {
	case <-ari.started:
	case <-time.After(3 * time.Second):
		t.Fatal("first invocation never started")
	}
	if err := rt.Send(ctx, control.PauseCommand{}); err != nil {
		t.Fatalf("Send(pause) error = %v", err)
	}

	// The in-flight invocation is not killed: it must be allowed to finish.
	snap, _ := rt.Snapshot(ctx)
	if snap.State != control.RunStatePauseRequested {
		t.Errorf("State = %q, want pause_requested while an invocation is in flight", snap.State)
	}

	close(ari.release)

	waitFor(t, "the loop to reach the paused state", func() bool {
		s, _ := rt.Snapshot(ctx)
		return s.State == control.RunStatePaused
	})

	callsWhilePaused := len(ari.prompts())

	if err := rt.Send(ctx, control.ResumeCommand{}); err != nil {
		t.Fatalf("Send(resume) error = %v", err)
	}
	waitFor(t, "further cycles after resume", func() bool {
		return len(ari.prompts()) > callsWhilePaused
	})

	cancel()
	<-rt.Done()
}

func TestRuntime_PauseIsIdempotentAndResumeWithoutPauseIsNotAnError(t *testing.T) {
	rt, _ := newTestRuntime(t, &scriptedRunner{}, &scriptedRunner{}, &scriptedDecider{})
	ctx := context.Background()

	if err := rt.Send(ctx, control.PauseCommand{}); err != nil {
		t.Fatalf("first pause: %v", err)
	}
	if err := rt.Send(ctx, control.PauseCommand{}); err != nil {
		t.Fatalf("second pause: %v", err)
	}
	if err := rt.Send(ctx, control.ResumeCommand{}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := rt.Send(ctx, control.ResumeCommand{}); err != nil {
		t.Fatalf("redundant resume: %v", err)
	}
}

// --- Manual QAC approval ---------------------------------------------

func TestRuntime_ManualApproval_BlocksUntilApprovedThenRunsAstra(t *testing.T) {
	ari := newGatedRunner(
		invocation.Envelope{Status: invocation.StatusEscalate, Escalation: highValueEscalation("which mechanism?")},
		invocation.Envelope{Status: invocation.StatusComplete},
	)
	astra := &scriptedRunner{envelopes: []invocation.Envelope{
		{Status: invocation.StatusContinue, Summary: "the confound is the shared seed"},
	}}

	bus := control.NewBus()
	t.Cleanup(bus.Close)
	store := inmemory.New()
	loop := &AriLoop{
		Ari: ari, Astra: astra, Memory: store, Events: bus,
		AriProfileName: "ari", AstraProfileName: "astra",
	}
	rt := NewRuntime(RuntimeConfig{
		Project: "p1", Loop: loop, Memory: store, Bus: bus,
		Logs: control.NewLogBus(100), MaxCycles: 5, QACMode: "manual",
		Agents: []control.AgentSnapshot{{Name: AgentAri}, {Name: AgentAstra}},
	})
	// The runtime is the Approver: manual mode means a human decides.
	loop.Decider = scheduler.New(scheduler.ControlManual, scheduler.NewBudget(0, 0, 0), rt, "astra")
	rt.budget = loop.Decider.(*scheduler.Scheduler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.Start(ctx)
	close(ari.release)

	var pendingID string
	waitFor(t, "an escalation to await approval", func() bool {
		snap, _ := rt.Snapshot(ctx)
		if snap.QAC.PendingEscalation == nil {
			return false
		}
		pendingID = snap.QAC.PendingEscalation.ID
		return true
	})

	// Astra must not have run on the strength of the request alone.
	if astra.callCount() != 0 {
		t.Fatalf("Astra ran before approval (%d calls)", astra.callCount())
	}

	snap, _ := rt.Snapshot(ctx)
	if snap.QAC.PendingEscalation.Question == "" {
		t.Error("expected the pending escalation to carry its question")
	}

	if err := rt.Send(ctx, control.ApproveEscalationCommand{EscalationID: pendingID}); err != nil {
		t.Fatalf("Send(approve) error = %v", err)
	}
	waitFor(t, "Astra to run after approval", func() bool { return astra.callCount() == 1 })

	<-rt.Done()
}

func TestRuntime_ManualDenial_KeepsAstraIdle(t *testing.T) {
	ari := newGatedRunner(
		invocation.Envelope{Status: invocation.StatusEscalate, Escalation: highValueEscalation("which mechanism?")},
		invocation.Envelope{Status: invocation.StatusComplete},
	)
	astra := &scriptedRunner{}

	bus := control.NewBus()
	t.Cleanup(bus.Close)
	store := inmemory.New()
	loop := &AriLoop{
		Ari: ari, Astra: astra, Memory: store, Events: bus,
		AriProfileName: "ari", AstraProfileName: "astra",
	}
	rt := NewRuntime(RuntimeConfig{
		Project: "p1", Loop: loop, Memory: store, Bus: bus,
		Logs: control.NewLogBus(100), MaxCycles: 5, QACMode: "manual",
		Agents: []control.AgentSnapshot{{Name: AgentAri}, {Name: AgentAstra}},
	})
	loop.Decider = scheduler.New(scheduler.ControlManual, scheduler.NewBudget(0, 0, 0), rt, "astra")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.Start(ctx)
	close(ari.release)

	var pendingID string
	waitFor(t, "a pending escalation", func() bool {
		snap, _ := rt.Snapshot(ctx)
		if snap.QAC.PendingEscalation == nil {
			return false
		}
		pendingID = snap.QAC.PendingEscalation.ID
		return true
	})

	if err := rt.Send(ctx, control.DenyEscalationCommand{
		EscalationID: pendingID,
		Reason:       "a cheaper discriminating experiment remains available",
	}); err != nil {
		t.Fatalf("Send(deny) error = %v", err)
	}

	<-rt.Done()
	if astra.callCount() != 0 {
		t.Errorf("expected a denied escalation to leave Astra idle, got %d calls", astra.callCount())
	}

	// The denial must reach the next Ari prompt as feedback.
	prompts := ari.prompts()
	if len(prompts) < 2 {
		t.Fatalf("expected a follow-up Ari cycle, got %d", len(prompts))
	}
	if !strings.Contains(prompts[1], "cheaper discriminating experiment remains available") {
		t.Errorf("expected the denial reason to reach Ari, got:\n%s", prompts[1])
	}
}

func TestRuntime_ApproveUnknownEscalationIsRejected(t *testing.T) {
	rt, _ := newTestRuntime(t, &scriptedRunner{}, &scriptedRunner{}, &scriptedDecider{})

	err := rt.Send(context.Background(), control.ApproveEscalationCommand{EscalationID: "nope"})
	if !errors.Is(err, control.ErrNoSuchEscalation) {
		t.Errorf("Send() = %v, want ErrNoSuchEscalation", err)
	}
}

// --- Snapshot ---------------------------------------------------------

func TestRuntime_Snapshot_ReportsAgentsCyclesAndResearchCounts(t *testing.T) {
	ari := newGatedRunner(
		invocation.Envelope{
			Status:             invocation.StatusContinue,
			Summary:            "found something",
			Frontier:           "frontier v2",
			Hypotheses:         []string{"h1", "h2"},
			RejectedHypotheses: []string{"r1"},
			OpenQuestions:      []string{"q1"},
			Confounds:          []string{"c1"},
			NextCandidateStep:  "E27",
		},
		invocation.Envelope{Status: invocation.StatusComplete, Summary: "done"},
	)
	rt, _ := newTestRuntime(t, ari, &scriptedRunner{}, &scriptedDecider{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Before starting: idle, no active agent, both agents listed.
	snap, err := rt.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snap.State != control.RunStateIdle {
		t.Errorf("State = %q, want idle", snap.State)
	}
	if len(snap.Agents) != 2 || snap.Agents[0].Name != AgentAri {
		t.Errorf("Agents = %+v, want ari then astra in configured order", snap.Agents)
	}
	if snap.Agents[0].Model != "m-ari" {
		t.Errorf("expected the configured model to be carried as a label, got %q", snap.Agents[0].Model)
	}

	rt.Start(ctx)
	select {
	case <-ari.started:
	case <-time.After(3 * time.Second):
		t.Fatal("invocation never started")
	}

	waitFor(t, "Ari to appear as the working agent", func() bool {
		s, _ := rt.Snapshot(ctx)
		return s.ActiveAgent == AgentAri && s.Agents[0].State == control.AgentWorking
	})

	close(ari.release)
	waitFor(t, "research counts to reflect the persisted step", func() bool {
		s, _ := rt.Snapshot(ctx)
		return s.ActiveHypotheses == 2 && s.RejectedCount == 1 && s.OpenQuestionCount == 1 && s.ConfoundCount == 1
	})

	snap, _ = rt.Snapshot(ctx)
	if snap.Frontier != "frontier v2" {
		t.Errorf("Frontier = %q", snap.Frontier)
	}
	if snap.CurrentExperiment != "E27" {
		t.Errorf("CurrentExperiment = %q", snap.CurrentExperiment)
	}
	if snap.MemoryBackend != "inmemory" {
		t.Errorf("MemoryBackend = %q, want the backend carried as metadata", snap.MemoryBackend)
	}

	cancel()
	<-rt.Done()
}

func TestRuntime_Snapshot_QACReportsUnlimitedBudgetDistinctlyFromExhausted(t *testing.T) {
	rt, _ := newTestRuntime(t, &scriptedRunner{}, &scriptedRunner{}, &scriptedDecider{})
	rt.budget = scheduler.New(scheduler.ControlAutomatic, scheduler.NewBudget(0, 0, 0), nil, "astra")

	snap, _ := rt.Snapshot(context.Background())
	if snap.QAC.CallsRemainingKnown {
		t.Error("an unconfigured budget must not report a known remaining count")
	}

	rt.budget = scheduler.New(scheduler.ControlAutomatic, scheduler.NewBudget(0, 4, 0), nil, "astra")
	snap, _ = rt.Snapshot(context.Background())
	if !snap.QAC.CallsRemainingKnown || snap.QAC.CallsRemaining != 4 {
		t.Errorf("QAC = %+v, want 4 remaining and known", snap.QAC)
	}
}

// --- Escalation projection --------------------------------------------

func TestRuntime_Escalations_RecordScoreAndDecision(t *testing.T) {
	ari := newGatedRunner(
		invocation.Envelope{Status: invocation.StatusEscalate, Escalation: highValueEscalation("which mechanism?")},
		invocation.Envelope{Status: invocation.StatusComplete},
	)
	astra := &scriptedRunner{envelopes: []invocation.Envelope{{Status: invocation.StatusContinue, Summary: "answer"}}}
	rt, _ := newTestRuntime(t, ari, astra,
		scheduler.New(scheduler.ControlAutomatic, scheduler.NewBudget(0, 0, 0), nil, "astra"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.Start(ctx)
	close(ari.release)
	<-rt.Done()

	escalations, err := rt.Escalations(ctx, 0)
	if err != nil {
		t.Fatalf("Escalations() error = %v", err)
	}
	if len(escalations) == 0 {
		t.Fatal("expected an escalation record")
	}

	e := escalations[0]
	if e.Question != "which mechanism?" {
		t.Errorf("Question = %q", e.Question)
	}
	if e.Decision != control.DecisionApproved {
		t.Errorf("Decision = %q, want approved", e.Decision)
	}
	// The QAC factors must be inspectable, not just the verdict.
	if e.Score <= 0 || e.Uncertainty <= 0 || e.Importance <= 0 || e.ExpectedGain <= 0 {
		t.Errorf("expected the scoring factors to be projected, got %+v", e)
	}
	if e.FailedLocalAttempts != 3 {
		t.Errorf("FailedLocalAttempts = %d, want 3", e.FailedLocalAttempts)
	}
}

// --- Logs and replay --------------------------------------------------

func TestRuntime_AgentLogsStayPerAgent(t *testing.T) {
	rt, _ := newTestRuntime(t, &scriptedRunner{}, &scriptedRunner{}, &scriptedDecider{})

	ariOut := rt.AgentWriter(AgentAri, control.StreamStdout)
	astraOut := rt.AgentWriter(AgentAstra, control.StreamStdout)

	ariOut.Write([]byte("loss=0.034\nremote influence audit passed\n"))
	astraOut.Write([]byte("considering the crossed design\n"))
	// A partial line must not be published until its newline arrives.
	ariOut.Write([]byte("partial"))

	ari, _ := rt.AgentLog(context.Background(), AgentAri, 0)
	if len(ari) != 2 {
		t.Fatalf("len(ari log) = %d, want 2 complete lines", len(ari))
	}
	if ari[0].Text != "loss=0.034" || ari[1].Text != "remote influence audit passed" {
		t.Errorf("ari log = %+v", ari)
	}

	astra, _ := rt.AgentLog(context.Background(), AgentAstra, 0)
	if len(astra) != 1 {
		t.Errorf("len(astra log) = %d, want 1", len(astra))
	}

	all, _ := rt.AgentLog(context.Background(), "", 0)
	if len(all) != 3 {
		t.Errorf("len(all logs) = %d, want 3", len(all))
	}
}

func TestJSONLReplayer_ReturnsRecentEventsAndToleratesGarbage(t *testing.T) {
	log := newEventLog(t)
	writer := log.writer()
	for i := 0; i < 5; i++ {
		writer.Log("research_cycle_started", map[string]any{"cycle": i})
	}
	log.file.WriteString("this is not json\n")
	writer.Log("escalation_denied", map[string]any{"reason": "too cheap"})

	replayer := &JSONLReplayer{Path: log.path}
	events, err := replayer.Replay(context.Background(), 3)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want the 3 most recent", len(events))
	}
	last := events[len(events)-1]
	if last.Name != "escalation_denied" || last.String("reason") != "too cheap" {
		t.Errorf("last event = %+v", last)
	}
	if last.Time.IsZero() {
		t.Error("expected the persisted timestamp to be parsed back")
	}
	// The reserved keys are lifted out of Fields so replay matches live.
	if _, ok := last.Fields["event"]; ok {
		t.Error("expected the event name to be lifted out of Fields")
	}
}

func TestJSONLReplayer_MissingLogIsNotAnError(t *testing.T) {
	replayer := &JSONLReplayer{Path: t.TempDir() + "/never-ran.jsonl"}
	events, err := replayer.Replay(context.Background(), 10)
	if err != nil {
		t.Fatalf("Replay() error = %v, want nil for a project that never ran", err)
	}
	if len(events) != 0 {
		t.Errorf("len(events) = %d, want 0", len(events))
	}
}

// The durable JSONL log must keep working unchanged when a live bus is
// attached alongside it: the log is the record, the bus is a convenience.
func TestMultiSink_WritesJSONLAndPublishesLive(t *testing.T) {
	log := newEventLog(t)
	bus := control.NewBus()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.Subscribe(ctx)

	sink := invocation.MultiSink{log.writer(), bus}
	sink.Log("escalation_approved", map[string]any{"score": 0.87})

	select {
	case ev := <-ch:
		if ev.Name != "escalation_approved" {
			t.Errorf("live event = %q", ev.Name)
		}
		// The JSONL writer mutates the map it is handed; a live subscriber
		// must not see those injected keys.
		if _, ok := ev.Fields["timestamp"]; ok {
			t.Error("subscriber observed the JSONL sink's mutation of the shared fields map")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the live event")
	}

	persisted := log.find("escalation_approved")
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted event, got %d", len(persisted))
	}
	if persisted[0]["timestamp"] == nil {
		t.Error("expected the durable log to keep stamping timestamps")
	}
}
