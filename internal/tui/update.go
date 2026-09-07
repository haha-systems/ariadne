package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/haha-systems/ariadne/internal/control"
	"github.com/haha-systems/ariadne/internal/invocation"
)

// Update routes messages. It never calls the controller inline: every
// runtime interaction is returned as a tea.Cmd, so the event loop keeps
// redrawing while research does whatever it is doing.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeInput()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case SnapshotMsg:
		if msg.Err != nil {
			m.setAlert("snapshot failed: %v", msg.Err)
			return m, nil
		}
		m.data.snapshot = msg.Snapshot
		if msg.Snapshot.LastError != "" {
			m.alert = msg.Snapshot.LastError
		}
		return m, nil

	case HistoryMsg:
		if msg.Err != nil {
			m.setAlert("history replay failed: %v", msg.Err)
		}
		// Replay lands before live events, and both are appended in
		// arrival order, so the timeline reads chronologically across the
		// boundary between historical and live.
		m.data.events = append(msg.Events, m.data.events...)
		m.data.logs = append(msg.Logs, m.data.logs...)
		m.timeline.notifyAppended(len(m.data.events))
		m.agents.notifyAppended()
		return m, nil

	case RuntimeEventMsg:
		m.appendEvent(msg.Event)
		return m, tea.Batch(m.reactToEvent(msg.Event), waitForEvent(m.eventCh))

	case EventsClosedMsg:
		m.data.live = false
		return m, nil

	case AgentLogMsg:
		m.appendLog(msg.Entry)
		return m, waitForLog(m.logCh)

	case LogsClosedMsg:
		return m, nil

	case EscalationsMsg:
		if msg.Err != nil {
			m.setAlert("escalations unavailable: %v", msg.Err)
			return m, nil
		}
		m.data.escalations = msg.Escalations
		return m, nil

	case MemoryMsg:
		if msg.Err != nil {
			m.setAlert("memory unavailable: %v", msg.Err)
			return m, nil
		}
		m.data.memory = msg.View
		return m, nil

	case SteeringMsg:
		if msg.Err != nil {
			m.setAlert("steering records unavailable: %v", msg.Err)
			return m, nil
		}
		m.data.steering = msg.Records
		return m, nil

	case CommandResultMsg:
		if msg.Err != nil {
			m.setAlert("%s failed: %v", msg.Command, msg.Err)
			return m, nil
		}
		m.setAlert("%s accepted", msg.Command)
		// A command's effect shows up in the projection, so refresh the
		// reads it could have changed.
		return m, tea.Batch(
			fetchSnapshot(m.ctx, m.controller),
			fetchSteering(m.ctx, m.controller),
			fetchEscalations(m.ctx, m.controller),
		)

	case TickMsg:
		cmds := []tea.Cmd{tick(), fetchSnapshot(m.ctx, m.controller)}
		if m.quitting && m.data.snapshot.State == control.RunStatePaused {
			// The pause-then-exit path: leave once research is safely at
			// a boundary rather than killing it mid-invocation.
			return m, tea.Quit
		}
		switch m.view {
		case viewMemory:
			cmds = append(cmds, fetchMemory(m.ctx, m.controller))
		case viewEscalations:
			cmds = append(cmds, fetchEscalations(m.ctx, m.controller))
		}
		return m, tea.Batch(cmds...)
	}

	return m, nil
}

// reactToEvent refreshes the reads an event implies have changed, so the
// interface stays current without polling everything every tick.
func (m *Model) reactToEvent(ev control.Event) tea.Cmd {
	switch ev.Name {
	case invocation.EventEscalationRequested,
		invocation.EventEscalationScored,
		invocation.EventEscalationApproved,
		invocation.EventEscalationDenied:
		return tea.Batch(fetchEscalations(m.ctx, m.controller), fetchSnapshot(m.ctx, m.controller))

	case invocation.EventSteeringReceived, invocation.EventSteeringApplied:
		return fetchSteering(m.ctx, m.controller)

	case invocation.EventMemoryWritten, invocation.EventControlReturnedToAri:
		return fetchSnapshot(m.ctx, m.controller)

	case invocation.EventMemoryWriteFailed, invocation.EventInvocationFailed:
		if msg := ev.String("error"); msg != "" {
			m.setAlert("%s: %s", ev.Name, msg)
		}
		return nil
	}
	return nil
}

// handleKey dispatches a keypress. Text-entry modes get first refusal on
// every key, so typing "p" into a steering message does not pause research.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch m.mode {
	case modeSteering, modeCommand, modeDenyReason:
		switch key {
		case "esc":
			m.endInput()
			return m, nil
		case "enter":
			return m, m.submitInput()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case modeHelp:
		m.mode = modeNormal
		return m, nil

	case modeQuitConfirm:
		return m.handleQuitConfirm(key)
	}

	return m.handleNormalKey(key)
}

func (m *Model) handleNormalKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "1":
		m.view = viewResearch
		return m, nil
	case "2":
		m.view = viewAgents
		return m, nil
	case "3":
		m.view = viewTimeline
		return m, nil
	case "4":
		m.view = viewEscalations
		return m, fetchEscalations(m.ctx, m.controller)
	case "5":
		m.view = viewMemory
		return m, fetchMemory(m.ctx, m.controller)

	case "tab":
		m.focus = (m.focus + 1) % m.paneCount()
		return m, nil
	case "shift+tab":
		m.focus = (m.focus - 1 + m.paneCount()) % m.paneCount()
		return m, nil

	case "s":
		return m, m.startInput(modeSteering, "steer > ", "instruction for the next research turn")
	case "/":
		return m, m.startInput(modeCommand, "> ", "/steer … | /pause | /resume | /status")

	case "p":
		return m, sendCommand(m.ctx, m.controller, control.PauseCommand{})
	case "r":
		return m, sendCommand(m.ctx, m.controller, control.ResumeCommand{})

	case "a":
		esc, ok := m.selectedEscalation()
		if !ok || esc.Decision != control.DecisionPending {
			m.setAlert("no escalation is awaiting a decision")
			return m, nil
		}
		return m, sendCommand(m.ctx, m.controller, control.ApproveEscalationCommand{EscalationID: esc.ID})

	case "d":
		esc, ok := m.selectedEscalation()
		if !ok || esc.Decision != control.DecisionPending {
			m.setAlert("no escalation is awaiting a decision")
			return m, nil
		}
		m.denyTarget = esc.ID
		return m, m.startInput(modeDenyReason, "deny reason > ", "optional — reaches the agent next cycle")

	case "?":
		m.mode = modeHelp
		return m, nil

	case "q", "ctrl+c":
		return m.requestQuit()

	case "esc":
		m.alert = ""
		return m, nil
	}

	return m, m.routeToScreen(key)
}

// routeToScreen forwards navigation keys to the active screen.
func (m *Model) routeToScreen(key string) tea.Cmd {
	switch m.view {
	case viewAgents:
		return m.agents.handleKey(key, &m.data)
	case viewTimeline:
		return m.timeline.handleKey(key, &m.data)
	case viewEscalations:
		return m.escalations.handleKey(key, &m.data)
	case viewMemory:
		return m.memory.handleKey(key, &m.data)
	default:
		return m.research.handleKey(key, &m.data)
	}
}

// requestQuit implements the quit contract: quitting the interface must
// never corrupt or silently abandon a research run.
func (m *Model) requestQuit() (tea.Model, tea.Cmd) {
	state := m.data.snapshot.State
	if state != control.RunStateRunning && state != control.RunStatePauseRequested {
		// Nothing is in flight; leaving is unambiguous.
		return m, tea.Quit
	}
	m.mode = modeQuitConfirm
	return m, nil
}

func (m *Model) handleQuitConfirm(key string) (tea.Model, tea.Cmd) {
	switch strings.ToLower(key) {
	case "p", "enter":
		// The safe default: ask research to stop at its next boundary,
		// then exit once it has. Nothing in flight is discarded.
		m.mode = modeNormal
		m.quitting = true
		m.setAlert("pausing at the next safe boundary, then exiting")
		return m, sendCommand(m.ctx, m.controller, control.PauseCommand{})

	case "d":
		// Detach: the run continues, the interface goes away. Safe here
		// only because the runtime, not the interface, owns the loop.
		m.mode = modeNormal
		return m, tea.Quit

	case "esc", "n":
		m.mode = modeNormal
		return m, nil
	}
	return m, nil
}

// paneCount is how many focusable panes the current view has.
func (m *Model) paneCount() int {
	switch m.view {
	case viewResearch:
		return 4
	default:
		return 2
	}
}
