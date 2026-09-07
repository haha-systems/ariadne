package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/haha-systems/ariadne/internal/control"
)

// TestCaptureScreens renders every screen with representative data and
// writes the result to ARIADNE_CAPTURE. It is a development aid for
// reviewing the interface without a terminal, skipped unless that variable
// is set.
func TestCaptureScreens(t *testing.T) {
	out := os.Getenv("ARIADNE_CAPTURE")
	if out == "" {
		t.Skip("set ARIADNE_CAPTURE=<path> to capture screen renders")
	}

	base := time.Date(2026, 9, 7, 22, 41, 4, 0, time.Local)

	c := newFakeController()
	c.snapshot = control.ResearchSnapshot{
		Project: "distributed-cognition",
		State:   control.RunStateRunning,
		Cycle:   14,
		Agents: []control.AgentSnapshot{
			{
				Name: "ari", Role: "routine", Model: "gpt-5-codex", Provider: "codex",
				State: control.AgentWorking, CurrentTask: "Running delayed-channel control experiment E27",
				StartedAt: time.Now().Add(-47 * time.Second),
				CostClass: "cheap", ScarcityClass: "plentiful",
				LastSummary: "delay alone does not explain the one-bit gap",
			},
			{
				Name: "astra", Role: "frontier", Model: "claude-opus-5", Provider: "claude",
				State: control.AgentIdle, CostClass: "expensive", ScarcityClass: "rare",
				LastSummary: "the confound is the shared random seed across arms",
			},
		},
		ActiveAgent:       "ari",
		Frontier:          "One-bit advantage survives delay matching. Nonlinearity remains the leading explanation; the shared-seed confound identified by Astra in cycle 11 is now controlled for.",
		ActiveHypotheses:  3,
		RejectedCount:     7,
		OpenQuestionCount: 2,
		ConfoundCount:     1,
		CurrentExperiment: "E27 — crossed design, independent seeds per arm",
		LatestResult:      "baseline matches within noise (loss=0.034)",
		QAC: control.QACSnapshot{
			Mode: "manual", CallsUsed: 1, CallsRemaining: 3, CallsRemainingKnown: true,
		},
		PendingSteering: 1,
		MemoryBackend:   "ghostdive",
	}

	c.escalations = []control.EscalationSummary{
		{
			ID: "esc-1", RequestedAt: base.Add(-18 * time.Minute),
			RequestingAgent: "ari", TargetAgent: "astra",
			Question: "Is the one-bit advantage an artifact of optimisation rather than communication?",
			Reason:   "three local ablations failed to separate the two explanations",
			Score:    0.31, Uncertainty: 0.4, Importance: 0.5, ExpectedGain: 0.3,
			FailedLocalAttempts: 1, CheaperTestAvailable: true,
			ScarcityClass:  "rare",
			Decision:       control.DecisionDenied,
			DecisionReason: "escalation scored 0.31, below the 0.50 continue baseline — routine work remains the better use of this cycle",
			DecidedAt:      base.Add(-18 * time.Minute),
			Budget:         control.QACSnapshot{CallsUsed: 0, CallsRemaining: 4, CallsRemainingKnown: true},
		},
		{
			ID: "esc-2", RequestedAt: base.Add(-3 * time.Minute),
			RequestingAgent: "ari", TargetAgent: "astra",
			Question: "Two causal explanations survive the crossed design. Which one holds?",
			Reason:   "routine work has exhausted the cheap discriminators",
			Score:    0.87, Uncertainty: 0.9, Importance: 0.9, ExpectedGain: 0.91,
			FailedLocalAttempts: 3, CheaperTestAvailable: false,
			ScarcityClass: "rare",
			Decision:      control.DecisionPending,
			Budget:        control.QACSnapshot{CallsUsed: 1, CallsRemaining: 3, CallsRemainingKnown: true},
		},
	}
	pending := c.escalations[1]
	c.snapshot.QAC.PendingEscalation = &pending

	c.memory = control.MemoryView{
		Backend:  "ghostdive",
		Frontier: control.MemoryRecord{Kind: "frontier", Summary: c.snapshot.Frontier, State: "observed", AgentID: "astra", CreatedAt: base.Add(-9 * time.Minute)},
		Observations: []control.MemoryRecord{
			{Kind: "observation", Summary: "E14 shows a one-bit advantage over the matched baseline", State: "observed", AgentID: "ari", CreatedAt: base.Add(-40 * time.Minute)},
			{Kind: "observation", Summary: "delay-matched monolithic baseline matches within noise (loss=0.034)", State: "observed", AgentID: "ari", CreatedAt: base.Add(-12 * time.Minute)},
		},
		ActiveHypotheses: []control.MemoryRecord{
			{Kind: "hypothesis", Summary: "nonlinearity in the channel explains the advantage", State: "candidate", AgentID: "ari", CreatedAt: base.Add(-30 * time.Minute)},
		},
		RejectedHypotheses: []control.MemoryRecord{
			{Kind: "hypothesis", Summary: "sampling noise explains the advantage", State: "rejected", AgentID: "ari", CreatedAt: base.Add(-35 * time.Minute)},
		},
		OpenQuestions: []control.MemoryRecord{
			{Kind: "observation", Subject: "open_question", Summary: "does the advantage survive a crossed design?", State: "observed", AgentID: "ari", CreatedAt: base.Add(-8 * time.Minute)},
		},
		Confounds: []control.MemoryRecord{
			{Kind: "observation", Subject: "confound", Summary: "both arms shared a random seed until cycle 12", State: "observed", AgentID: "astra", CreatedAt: base.Add(-20 * time.Minute)},
		},
		Decisions: []control.MemoryRecord{
			{Kind: "decision", Summary: "the confound is the shared random seed across arms", State: "accepted", AgentID: "astra", CreatedAt: base.Add(-20 * time.Minute), EvidenceRefs: []string{"rec-14", "rec-15"}},
		},
	}

	events := []control.Event{
		{Name: "research_cycle_started", Time: base, Fields: map[string]any{"cycle": 14, "project": "distributed-cognition"}},
		{Name: "ari_identity", Time: base.Add(time.Second), Fields: map[string]any{"agent": "ari", "not": "astra", "profile": "ari", "cycle": 14}},
		{Name: "ari_invocation_started", Time: base.Add(time.Second), Fields: map[string]any{"agent": "ari", "cycle": 14, "model": "gpt-5-codex"}},
		{Name: "memory_written", Time: base.Add(2*time.Minute + 14*time.Second), Fields: map[string]any{"kind": "observation", "subject": "observation"}},
		{Name: "steering_received", Time: base.Add(2*time.Minute + 20*time.Second), Fields: map[string]any{"message": "Test a monolithic delay-matched baseline before changing the architecture.", "source": "user"}},
		{Name: "steering_applied", Time: base.Add(2*time.Minute + 21*time.Second), Fields: map[string]any{"cycle": 14}},
		{Name: "ari_invocation_completed", Time: base.Add(3 * time.Minute), Fields: map[string]any{"agent": "ari", "status": "escalate", "work_completed": "crossed design run"}},
		{Name: "escalation_requested", Time: base.Add(3*time.Minute + 1*time.Second), Fields: map[string]any{"question": "Two causal explanations survive the crossed design. Which one holds?", "failed_local_attempts": 3}},
		{Name: "escalation_scored", Time: base.Add(3*time.Minute + 1*time.Second), Fields: map[string]any{"score": 0.87, "score_uncertainty": 0.315, "score_importance": 0.315, "score_expected_gain": 0.273, "duplicate_of_denied": false}},
	}

	logs := []control.AgentLogEntry{
		{Agent: "ari", Stream: "system", Time: base.Add(time.Second), Text: "invocation started: routine research step"},
		{Agent: "ari", Stream: "stdout", Time: base.Add(30 * time.Second), Text: "running delayed-channel control E27..."},
		{Agent: "ari", Stream: "stdout", Time: base.Add(70 * time.Second), Text: "epoch 12  loss=0.041"},
		{Agent: "ari", Stream: "stdout", Time: base.Add(95 * time.Second), Text: "epoch 24  loss=0.034"},
		{Agent: "ari", Stream: "stdout", Time: base.Add(2 * time.Minute), Text: "remote influence audit passed"},
		{Agent: "ari", Stream: "stdout", Time: base.Add(2*time.Minute + 10*time.Second), Text: "baseline delta within noise; two explanations still live"},
		{Agent: "astra", Stream: "system", Time: base.Add(-20 * time.Minute), Text: "invocation completed: continue"},
	}

	newModel := func(w, h int) *Model {
		m := New(t.Context(), c)
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.Update(SnapshotMsg{Snapshot: c.snapshot})
		m.Update(EscalationsMsg{Escalations: c.escalations})
		m.Update(MemoryMsg{View: c.memory})
		m.Update(HistoryMsg{Events: events, Logs: logs})
		return m
	}

	var b strings.Builder
	capture := func(title string, w, h int, prep func(*Model)) {
		m := newModel(w, h)
		prep(m)
		fmt.Fprintf(&b, "\n%s  (%dx%d)\n%s\n%s\n",
			title, w, h, strings.Repeat("=", maxInt(len(title)+12, 40)), m.render())
	}

	capture("1. RESEARCH  — default view", 120, 32, func(m *Model) {})
	capture("2. AGENTS    — live log, following Ari", 120, 32, func(m *Model) { m.view = viewAgents })
	capture("3. TIMELINE  — structured runtime events", 120, 32, func(m *Model) {
		m.view = viewTimeline
		m.timeline.cursor = len(events) - 2
		m.timeline.pinned = true
	})
	capture("4. ESCALATIONS — pending approval, manual QAC", 120, 32, func(m *Model) { m.view = viewEscalations })
	capture("5. MEMORY    — read-only research record", 120, 32, func(m *Model) { m.view = viewMemory })
	capture("STEERING INPUT", 120, 20, func(m *Model) {
		m.startInput(modeSteering, "steer > ", "instruction for the next research turn")
		for _, r := range "Test the delayed-state control before changing architecture" {
			m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	})
	capture("QUIT CONFIRMATION — research still running", 120, 20, func(m *Model) { m.mode = modeQuitConfirm })
	capture("HELP", 120, 40, func(m *Model) { m.mode = modeHelp })
	capture("NARROW TERMINAL — panes stack", 60, 30, func(m *Model) {})
	capture("TOO SMALL — readable warning, not garbage", 30, 8, func(m *Model) {})

	if err := os.WriteFile(out, []byte(b.String()), 0644); err != nil {
		t.Fatalf("write capture: %v", err)
	}
	t.Logf("wrote %d bytes to %s", b.Len(), out)
}
