package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/haha-systems/ariadne/internal/control"
)

// fakeController is a hand-written control.ResearchController that records
// the commands it receives. The model is exercised against it directly, with
// no terminal involved: a TUI whose logic can only be tested by driving a
// real terminal is a TUI whose logic is not really tested.
type fakeController struct {
	mu sync.Mutex

	snapshot    control.ResearchSnapshot
	escalations []control.EscalationSummary
	memory      control.MemoryView
	steering    []control.SteeringRecord
	events      []control.Event
	logs        []control.AgentLogEntry

	sent    []control.ResearchCommand
	sendErr error

	snapshotErr error
	subscribeNo bool

	eventCh chan control.Event
	logCh   chan control.AgentLogEntry
}

func newFakeController() *fakeController {
	return &fakeController{
		snapshot: control.ResearchSnapshot{
			Project: "distributed-cognition",
			State:   control.RunStateRunning,
			Cycle:   14,
			Agents: []control.AgentSnapshot{
				{Name: "ari", Role: "routine", Model: "m-ari", Provider: "codex", State: control.AgentWorking, CurrentTask: "delayed-channel control"},
				{Name: "astra", Role: "frontier", Model: "m-astra", Provider: "claude", State: control.AgentIdle, ScarcityClass: "rare"},
			},
			ActiveAgent:       "ari",
			Frontier:          "one-bit advantage explained by nonlinearity",
			ActiveHypotheses:  3,
			RejectedCount:     7,
			OpenQuestionCount: 2,
			CurrentExperiment: "E27",
			QAC:               control.QACSnapshot{Mode: "manual", CallsUsed: 1, CallsRemaining: 3, CallsRemainingKnown: true},
			MemoryBackend:     "ghostdive",
		},
		eventCh: make(chan control.Event, 16),
		logCh:   make(chan control.AgentLogEntry, 16),
	}
}

func (f *fakeController) Snapshot(context.Context) (control.ResearchSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapshotErr != nil {
		return control.ResearchSnapshot{}, f.snapshotErr
	}
	return f.snapshot, nil
}

func (f *fakeController) Subscribe(context.Context) (<-chan control.Event, error) {
	if f.subscribeNo {
		return nil, errors.New("no bus")
	}
	return f.eventCh, nil
}

func (f *fakeController) SubscribeLogs(context.Context) (<-chan control.AgentLogEntry, error) {
	if f.subscribeNo {
		return nil, errors.New("no bus")
	}
	return f.logCh, nil
}

func (f *fakeController) RecentEvents(_ context.Context, _ int) ([]control.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events, nil
}

func (f *fakeController) AgentLog(_ context.Context, agent string, _ int) ([]control.AgentLogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if agent == "" {
		return f.logs, nil
	}
	var out []control.AgentLogEntry
	for _, e := range f.logs {
		if e.Agent == agent {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeController) Escalations(_ context.Context, _ int) ([]control.EscalationSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.escalations, nil
}

func (f *fakeController) Memory(context.Context) (control.MemoryView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.memory, nil
}

func (f *fakeController) Steering(context.Context) ([]control.SteeringRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.steering, nil
}

func (f *fakeController) Send(_ context.Context, cmd control.ResearchCommand) error {
	if err := control.Validate(cmd); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, cmd)
	return nil
}

func (f *fakeController) sentCommands() []control.ResearchCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]control.ResearchCommand(nil), f.sent...)
}

// --- test harness -----------------------------------------------------

// newTestModel builds a model sized for a wide terminal with its first
// snapshot already applied.
func newTestModel(t *testing.T, c *fakeController) *Model {
	t.Helper()
	m := New(context.Background(), c)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	snap, _ := c.Snapshot(context.Background())
	m.Update(SnapshotMsg{Snapshot: snap})
	return m
}

// press sends a keypress and runs the resulting command chain, so a test
// observes the same end state a user would.
func press(t *testing.T, m *Model, key string) {
	t.Helper()
	_, cmd := m.Update(keyPress(key))
	runCmd(t, m, cmd)
}

// keyPress builds a KeyPressMsg whose String() matches key. Bubble Tea v2
// keys carry structured fields, so the text and code are set explicitly
// rather than relying on zero values.
func keyPress(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		r := []rune(key)[0]
		return tea.KeyPressMsg{Code: r, Text: string(r)}
	}
}

// typeText enters a string one keypress at a time. The command a keystroke
// returns is the text input's cursor-blink timer, which is deliberately not
// run: it produces no state a test cares about and only sleeps.
func typeText(t *testing.T, m *Model, text string) {
	t.Helper()
	for _, r := range text {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// runCmd executes a tea.Cmd and feeds any resulting message back into the
// model, following batches one level deep — enough for the model's own
// command shapes.
func runCmd(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	switch msg := msg.(type) {
	case nil:
		return
	case tea.BatchMsg:
		for _, sub := range msg {
			runCmdShallow(t, m, sub)
		}
	default:
		_, next := m.Update(msg)
		runCmdShallow(t, m, next)
	}
}

// runCmdShallow executes one command without recursing further, so a
// self-rearming stream command cannot loop forever in a test.
func runCmdShallow(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	select {
	case msg := <-done:
		if msg == nil {
			return
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				runCmdShallow(t, m, sub)
			}
			return
		}
		m.Update(msg)
	case <-time.After(20 * time.Millisecond):
		// A blocking stream receive; nothing to apply.
	}
}

// --- screen switching -------------------------------------------------

func TestModel_NumberKeysSwitchScreens(t *testing.T) {
	m := newTestModel(t, newFakeController())

	for key, want := range map[string]view{
		"1": viewResearch,
		"2": viewAgents,
		"3": viewTimeline,
		"4": viewEscalations,
		"5": viewMemory,
	} {
		press(t, m, key)
		if m.view != want {
			t.Errorf("key %q selected %v, want %v", key, m.view, want)
		}
	}
}

func TestModel_TabCyclesFocusAndWrapsAround(t *testing.T) {
	m := newTestModel(t, newFakeController())
	m.view = viewResearch

	start := m.focus
	for i := 0; i < m.paneCount(); i++ {
		press(t, m, "tab")
	}
	if m.focus != start {
		t.Errorf("focus = %d after a full cycle, want it back at %d", m.focus, start)
	}

	press(t, m, "shift+tab")
	if m.focus != m.paneCount()-1 {
		t.Errorf("shift+tab from %d gave %d, want wrap to %d", start, m.focus, m.paneCount()-1)
	}
}

// --- steering ---------------------------------------------------------

func TestModel_SteeringInputSendsSteerCommand(t *testing.T) {
	c := newFakeController()
	m := newTestModel(t, c)

	press(t, m, "s")
	if m.mode != modeSteering {
		t.Fatalf("mode = %v, want steering", m.mode)
	}

	const instruction = "test the delayed-state control first"
	typeText(t, m, instruction)
	press(t, m, "enter")

	sent := c.sentCommands()
	if len(sent) != 1 {
		t.Fatalf("sent %d commands, want 1", len(sent))
	}
	steer, ok := sent[0].(control.SteerCommand)
	if !ok {
		t.Fatalf("sent %T, want SteerCommand", sent[0])
	}
	if steer.Message != instruction {
		t.Errorf("Message = %q", steer.Message)
	}
	if m.mode != modeNormal {
		t.Errorf("mode = %v after submit, want normal", m.mode)
	}
}

// While typing, ordinary letters must reach the text box rather than
// triggering pause/approve/deny.
func TestModel_KeysDuringTextEntryDoNotTriggerActions(t *testing.T) {
	c := newFakeController()
	m := newTestModel(t, c)

	press(t, m, "s")
	typeText(t, m, "pause and approve and deny")

	if got := c.sentCommands(); len(got) != 0 {
		t.Fatalf("typing sent %d commands, want none", len(got))
	}
	if !strings.Contains(m.input.Value(), "pause and approve") {
		t.Errorf("input = %q, want the typed text", m.input.Value())
	}

	press(t, m, "esc")
	if m.mode != modeNormal {
		t.Errorf("esc did not leave text entry")
	}
	if len(c.sentCommands()) != 0 {
		t.Error("cancelling text entry must send nothing")
	}
}

func TestModel_EmptySteeringIsNotSent(t *testing.T) {
	c := newFakeController()
	m := newTestModel(t, c)

	press(t, m, "s")
	typeText(t, m, "   ")
	press(t, m, "enter")

	if got := c.sentCommands(); len(got) != 0 {
		t.Errorf("sent %d commands for empty steering, want 0", len(got))
	}
}

func TestModel_SlashCommandsMapToRuntimeCommands(t *testing.T) {
	for _, tc := range []struct {
		typed string
		want  string
	}{
		{"/pause", "pause"},
		{"/resume", "resume"},
		{"/steer do the thing", "steer"},
		{"plain text with no verb", "steer"},
	} {
		c := newFakeController()
		m := newTestModel(t, c)

		press(t, m, "/")
		typeText(t, m, tc.typed)
		press(t, m, "enter")

		sent := c.sentCommands()
		if len(sent) != 1 {
			t.Fatalf("%q: sent %d commands, want 1", tc.typed, len(sent))
		}
		if got := control.CommandName(sent[0]); got != tc.want {
			t.Errorf("%q produced %q, want %q", tc.typed, got, tc.want)
		}
	}
}

// --- pause and resume -------------------------------------------------

func TestModel_PauseAndResumeKeysSendCommands(t *testing.T) {
	c := newFakeController()
	m := newTestModel(t, c)

	press(t, m, "p")
	press(t, m, "r")

	sent := c.sentCommands()
	if len(sent) != 2 {
		t.Fatalf("sent %d commands, want 2", len(sent))
	}
	if control.CommandName(sent[0]) != "pause" || control.CommandName(sent[1]) != "resume" {
		t.Errorf("commands = %q, %q", control.CommandName(sent[0]), control.CommandName(sent[1]))
	}
}

// --- QAC approval -----------------------------------------------------

func pendingEscalation() control.EscalationSummary {
	return control.EscalationSummary{
		ID:              "esc-1",
		RequestedAt:     time.Now(),
		RequestingAgent: "ari",
		TargetAgent:     "astra",
		Question:        "Two causal explanations survive.",
		Score:           0.87,
		Uncertainty:     0.9,
		Importance:      0.9,
		ExpectedGain:    0.91,
		Decision:        control.DecisionPending,
		Budget:          control.QACSnapshot{CallsUsed: 1, CallsRemaining: 3, CallsRemainingKnown: true},
	}
}

func TestModel_ApproveKeySendsApprovalForPendingEscalation(t *testing.T) {
	c := newFakeController()
	esc := pendingEscalation()
	c.snapshot.QAC.PendingEscalation = &esc
	m := newTestModel(t, c)

	press(t, m, "a")

	sent := c.sentCommands()
	if len(sent) != 1 {
		t.Fatalf("sent %d commands, want 1", len(sent))
	}
	approve, ok := sent[0].(control.ApproveEscalationCommand)
	if !ok {
		t.Fatalf("sent %T, want ApproveEscalationCommand", sent[0])
	}
	if approve.EscalationID != "esc-1" {
		t.Errorf("EscalationID = %q", approve.EscalationID)
	}
}

func TestModel_DenyKeyPromptsForReasonThenSendsDenial(t *testing.T) {
	c := newFakeController()
	esc := pendingEscalation()
	c.snapshot.QAC.PendingEscalation = &esc
	m := newTestModel(t, c)

	press(t, m, "d")
	if m.mode != modeDenyReason {
		t.Fatalf("mode = %v, want a deny-reason prompt", m.mode)
	}

	typeText(t, m, "a cheaper test remains")
	press(t, m, "enter")

	sent := c.sentCommands()
	if len(sent) != 1 {
		t.Fatalf("sent %d commands, want 1", len(sent))
	}
	deny, ok := sent[0].(control.DenyEscalationCommand)
	if !ok {
		t.Fatalf("sent %T, want DenyEscalationCommand", sent[0])
	}
	if deny.EscalationID != "esc-1" || deny.Reason != "a cheaper test remains" {
		t.Errorf("deny = %+v", deny)
	}
}

func TestModel_ApproveWithNothingPendingExplainsRatherThanSending(t *testing.T) {
	c := newFakeController()
	m := newTestModel(t, c)

	press(t, m, "a")

	if got := c.sentCommands(); len(got) != 0 {
		t.Errorf("sent %d commands with nothing pending, want 0", len(got))
	}
	if !strings.Contains(m.alert, "awaiting") {
		t.Errorf("alert = %q, want an explanation", m.alert)
	}
}

// The escalations view must show the factors behind a decision, not just
// the verdict — QAC is only inspectable if its reasoning is visible.
func TestModel_EscalationsViewShowsScoringFactors(t *testing.T) {
	c := newFakeController()
	esc := pendingEscalation()
	esc.Decision = control.DecisionDenied
	esc.DecisionReason = "scored below the continue baseline"
	esc.CheaperTestAvailable = true
	c.escalations = []control.EscalationSummary{esc}
	m := newTestModel(t, c)

	m.Update(EscalationsMsg{Escalations: c.escalations})
	press(t, m, "4")
	out := m.render()

	for _, want := range []string{
		"DENIED",
		"uncertainty",
		"importance",
		"expected gain",
		"cheaper test available",
		"scored below the continue baseline",
		"0.91",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("escalations view missing %q", want)
		}
	}
}

// --- rendering --------------------------------------------------------

func TestModel_HeaderShowsStatusFieldsAsWords(t *testing.T) {
	m := newTestModel(t, newFakeController())
	out := m.render()

	for _, want := range []string{
		"distributed-cognition",
		"RUNNING",
		"cycle 14",
		"ari",
		"QAC:manual",
		"Astra 1/4",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("header missing %q\n%s", want, out)
		}
	}
}

func TestModel_UnlimitedBudgetRendersDistinctlyFromExhausted(t *testing.T) {
	c := newFakeController()
	c.snapshot.QAC = control.QACSnapshot{Mode: "auto", CallsUsed: 2, CallsRemainingKnown: false}
	m := newTestModel(t, c)
	if !strings.Contains(m.render(), "no limit") {
		t.Error("an unlimited budget must not render as though it were exhausted")
	}

	c.snapshot.QAC = control.QACSnapshot{Mode: "auto", CallsUsed: 4, CallsRemaining: 0, CallsRemainingKnown: true}
	m = newTestModel(t, c)
	if !strings.Contains(m.render(), "EXHAUSTED") {
		t.Error("an exhausted budget must say so in words")
	}
}

func TestModel_ResearchViewShowsStateWithoutNavigation(t *testing.T) {
	m := newTestModel(t, newFakeController())
	out := m.render()

	for _, want := range []string{
		"AGENTS", "ACTIVE AGENT", "RESEARCH STATE", "EVENT LOG",
		"Active hypotheses: 3",
		"Rejected:          7",
		"Open questions:    2",
		"E27",
		"nonlinearity",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("research view missing %q", want)
		}
	}
}

// --- resize -----------------------------------------------------------

func TestModel_LayoutAdaptsToTerminalWidth(t *testing.T) {
	m := newTestModel(t, newFakeController())

	for _, size := range []struct{ w, h int }{
		{160, 50}, // wide
		{90, 30},  // medium
		{50, 20},  // narrow
	} {
		m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		out := m.render()

		if out == "" {
			t.Fatalf("%dx%d rendered nothing", size.w, size.h)
		}
		for i, line := range strings.Split(out, "\n") {
			if got := lineWidth(line); got > size.w {
				t.Errorf("%dx%d: line %d is %d cells wide, want <= %d", size.w, size.h, i, got, size.w)
			}
		}
		if lines := strings.Count(out, "\n") + 1; lines > size.h {
			t.Errorf("%dx%d: rendered %d lines, want <= %d", size.w, size.h, lines, size.h)
		}
	}
}

func TestModel_TooSmallTerminalWarnsInsteadOfRenderingGarbage(t *testing.T) {
	m := newTestModel(t, newFakeController())
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 5})

	out := m.render()
	if !strings.Contains(out, "too small") {
		t.Errorf("expected a readable warning, got:\n%s", out)
	}
	if !strings.Contains(out, "q to quit") {
		t.Error("the warning should still say how to get out")
	}
}

// --- errors -----------------------------------------------------------

func TestModel_RuntimeErrorsAppearInTheAlertAreaWithoutCrashing(t *testing.T) {
	c := newFakeController()
	m := newTestModel(t, c)

	m.Update(SnapshotMsg{Err: errors.New("ghostdive unavailable")})
	if !strings.Contains(m.alert, "ghostdive unavailable") {
		t.Errorf("alert = %q", m.alert)
	}
	if !strings.Contains(m.render(), "ghostdive unavailable") {
		t.Error("expected the error to be visible in the alert area")
	}

	// A previous good snapshot must survive a failed refresh: the interface
	// degrades rather than blanking.
	if m.data.snapshot.Project != "distributed-cognition" {
		t.Error("a failed snapshot discarded the last good one")
	}

	press(t, m, "esc")
	if m.alert != "" {
		t.Error("esc should dismiss the alert")
	}
}

func TestModel_FailedCommandSurfacesAsAnAlert(t *testing.T) {
	c := newFakeController()
	c.sendErr = errors.New("research loop is not running")
	m := newTestModel(t, c)

	press(t, m, "p")
	if !strings.Contains(m.alert, "research loop is not running") {
		t.Errorf("alert = %q, want the command failure", m.alert)
	}
}

func TestModel_MissingEventStreamIsReportedNotFatal(t *testing.T) {
	c := newFakeController()
	c.subscribeNo = true
	m := New(context.Background(), c)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Init()

	if m.data.live {
		t.Error("expected the model to know it is not receiving live events")
	}
	if !strings.Contains(m.render(), "not live") {
		t.Error("expected the header to say events are not live")
	}
}

// --- quit -------------------------------------------------------------

func TestModel_QuitWhileRunningOffersPauseOrDetach(t *testing.T) {
	c := newFakeController()
	m := newTestModel(t, c)

	press(t, m, "q")
	if m.mode != modeQuitConfirm {
		t.Fatalf("mode = %v, want a quit confirmation while research runs", m.mode)
	}

	out := m.render()
	for _, want := range []string{"Pause at the next safe boundary", "Detach", "Cancel"} {
		if !strings.Contains(out, want) {
			t.Errorf("quit prompt missing %q", want)
		}
	}

	// The safe default pauses first rather than abandoning a running agent.
	press(t, m, "p")
	sent := c.sentCommands()
	if len(sent) != 1 || control.CommandName(sent[0]) != "pause" {
		t.Errorf("expected a pause before exit, got %v", sent)
	}
}

func TestModel_QuitWhenStoppedLeavesImmediately(t *testing.T) {
	c := newFakeController()
	c.snapshot.State = control.RunStateStopped
	m := newTestModel(t, c)

	_, cmd := m.Update(keyPress("q"))
	if cmd == nil {
		t.Fatal("expected a quit command when nothing is running")
	}
	if m.mode == modeQuitConfirm {
		t.Error("no confirmation is needed when research is not running")
	}
}

// --- streams ----------------------------------------------------------

func TestModel_EventsAndLogsAccumulateSeparately(t *testing.T) {
	m := newTestModel(t, newFakeController())

	m.Update(RuntimeEventMsg{Event: control.Event{Name: "research_cycle_started", Time: time.Now(), Fields: map[string]any{"cycle": 14}}})
	m.Update(AgentLogMsg{Entry: control.AgentLogEntry{Agent: "ari", Text: "loss=0.034", Time: time.Now()}})

	if len(m.data.events) != 1 {
		t.Errorf("len(events) = %d, want 1", len(m.data.events))
	}
	if len(m.data.logs) != 1 {
		t.Errorf("len(logs) = %d, want 1", len(m.data.logs))
	}

	// Structured events and raw output are different data models and must
	// not be merged into one another.
	if m.data.events[0].Name != "research_cycle_started" {
		t.Errorf("event = %+v", m.data.events[0])
	}
	if m.data.logs[0].Text != "loss=0.034" {
		t.Errorf("log = %+v", m.data.logs[0])
	}
}

func TestModel_HistoryReplayLandsBeforeLiveEvents(t *testing.T) {
	m := newTestModel(t, newFakeController())

	live := control.Event{Name: "live", Time: time.Now()}
	m.Update(RuntimeEventMsg{Event: live})
	m.Update(HistoryMsg{Events: []control.Event{{Name: "historical", Time: time.Now().Add(-time.Hour)}}})

	if len(m.data.events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(m.data.events))
	}
	if m.data.events[0].Name != "historical" || m.data.events[1].Name != "live" {
		t.Errorf("expected replay before live, got %q then %q", m.data.events[0].Name, m.data.events[1].Name)
	}
}

func TestModel_EventBufferIsBounded(t *testing.T) {
	m := newTestModel(t, newFakeController())
	for i := 0; i < maxBuffered+500; i++ {
		m.appendEvent(control.Event{Name: "e", Time: time.Now()})
	}
	if len(m.data.events) > maxBuffered {
		t.Errorf("len(events) = %d, want it bounded at %d", len(m.data.events), maxBuffered)
	}
}

// --- agent logs -------------------------------------------------------

func TestModel_AgentLogViewKeepsStreamsSeparateAndOffersAll(t *testing.T) {
	c := newFakeController()
	m := newTestModel(t, c)

	m.Update(AgentLogMsg{Entry: control.AgentLogEntry{Agent: "ari", Text: "ari-line", Time: time.Now()}})
	m.Update(AgentLogMsg{Entry: control.AgentLogEntry{Agent: "astra", Text: "astra-line", Time: time.Now()}})

	press(t, m, "2")
	out := m.render()
	if !strings.Contains(out, "ari-line") {
		t.Error("expected the selected agent's output")
	}
	if strings.Contains(out, "astra-line") {
		t.Error("Ari's log must not include Astra's output")
	}

	// n cycles the selection: ari -> astra -> all agents.
	press(t, m, "n")
	if !strings.Contains(m.render(), "astra-line") {
		t.Error("expected Astra's log after switching")
	}
	press(t, m, "n")
	all := m.render()
	if !strings.Contains(all, "ari-line") || !strings.Contains(all, "astra-line") {
		t.Error("the all-agents view should show both streams")
	}
}

func TestModel_ScrollingBackTurnsOffFollowMode(t *testing.T) {
	m := newTestModel(t, newFakeController())
	press(t, m, "2")

	for i := 0; i < 30; i++ {
		m.Update(AgentLogMsg{Entry: control.AgentLogEntry{Agent: "ari", Text: "line", Time: time.Now()}})
	}
	if !m.agents.follow {
		t.Fatal("expected follow mode by default")
	}

	press(t, m, "j")
	if m.agents.follow {
		t.Error("scrolling back should stop the log auto-following")
	}
	if !strings.Contains(m.render(), "paused") {
		t.Error("the log title should say it is no longer following")
	}

	press(t, m, "k")
	if !m.agents.follow {
		t.Error("returning to the newest line should resume following")
	}
}

// --- memory -----------------------------------------------------------

func TestModel_MemoryViewIsReadOnlyAndSectioned(t *testing.T) {
	c := newFakeController()
	c.memory = control.MemoryView{
		Backend:            "ghostdive",
		Frontier:           control.MemoryRecord{Kind: "frontier", Summary: "current frontier text"},
		Observations:       []control.MemoryRecord{{Kind: "observation", Summary: "E14 shows an advantage", State: "observed"}},
		ActiveHypotheses:   []control.MemoryRecord{{Kind: "hypothesis", Summary: "nonlinearity", State: "candidate"}},
		RejectedHypotheses: []control.MemoryRecord{{Kind: "hypothesis", Summary: "sampling noise", State: "rejected"}},
		OpenQuestions:      []control.MemoryRecord{{Kind: "observation", Summary: "does it survive?", State: "observed"}},
	}
	m := newTestModel(t, c)
	m.Update(MemoryMsg{View: c.memory})
	press(t, m, "5")

	out := m.render()
	for _, want := range []string{
		"FRONTIER", "LATEST OBSERVATIONS", "ACTIVE HYPOTHESES", "REJECTED HYPOTHESES", "OPEN QUESTIONS",
		"current frontier text", "sampling noise",
		"ghostdive", // backend shown as metadata only
		"read-only", // and the view says so
	} {
		if !strings.Contains(out, want) {
			t.Errorf("memory view missing %q", want)
		}
	}
}

// --- accessibility ----------------------------------------------------

// Every state colour distinguishes must also be spelled out, so the
// interface stays usable in a monochrome terminal or in copied text.
func TestModel_StatesAreReadableWithoutColour(t *testing.T) {
	for _, tc := range []struct {
		state control.RunState
		want  string
	}{
		{control.RunStateRunning, "RUNNING"},
		{control.RunStatePaused, "PAUSED"},
		{control.RunStatePauseRequested, "PAUSE REQUESTED"},
		{control.RunStateFailed, "FAILED"},
	} {
		c := newFakeController()
		c.snapshot.State = tc.state
		m := newTestModel(t, c)
		if !strings.Contains(stripANSI(m.render()), tc.want) {
			t.Errorf("state %q does not appear as the word %q", tc.state, tc.want)
		}
	}
}

func TestModel_HelpListsTheKeysAndTheSafetyRules(t *testing.T) {
	m := newTestModel(t, newFakeController())
	press(t, m, "?")

	out := stripANSI(m.render())
	for _, want := range []string{
		"1 ", "s ", "a ", "d ", "p ", "r ", "q ",
		"Pausing never kills a running agent",
		"Steering is part of the research record",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q", want)
		}
	}

	press(t, m, "x")
	if m.mode != modeNormal {
		t.Error("any key should close help")
	}
}

// stripANSI removes escape sequences so a test can assert on the text a
// monochrome terminal or a copy-paste would produce.
func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape && (r == 'm' || r == 'K' || r == 'H'):
			inEscape = false
		case !inEscape:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// lineWidth measures a rendered line in display cells, ignoring escapes.
func lineWidth(line string) int {
	return len([]rune(stripANSI(line)))
}
