package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/haha-systems/ariadne/internal/control"
	"github.com/haha-systems/ariadne/internal/invocation"
	"github.com/haha-systems/ariadne/internal/session"
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

	case SessionsMsg:
		if msg.Err != nil {
			m.setAlert("session list unavailable: %v", msg.Err)
			return m, nil
		}
		m.data.sessions = msg.Sessions
		m.sessionsScreen.clamp(&m.data)
		// Opening the harness with exactly one session should not make the
		// user pick it out of a list of one.
		if !m.attached() && len(msg.Sessions) == 1 {
			return m, m.attach(msg.Sessions[0].ID)
		}
		return m, nil

	case SessionChangeMsg:
		m.applySessionChange(msg.Change)
		return m, tea.Batch(m.reactToSessionChange(msg.Change), waitForSessionChange(m.sessionCh))

	case TaskStartedMsg:
		if msg.Err != nil {
			m.setAlert("could not start task: %v", msg.Err)
			return m, nil
		}
		m.setAlert("queued: %s", msg.Title)
		return m, fetchSessions(m.ctx, m.host)

	case SessionsClosedMsg:
		m.setAlert("session updates ended")
		return m, nil

	case CommandResultMsg:
		if msg.Err != nil {
			m.setAlert("%s failed: %v", msg.Command, msg.Err)
			return m, nil
		}
		m.setAlert("%s accepted", msg.Command)
		// A command's effect shows up in the projection, so refresh the
		// reads it could have changed.
		cmds := []tea.Cmd{fetchSnapshot(m.sessionCtx, m.controller)}
		if m.can(session.CapSteer) {
			cmds = append(cmds, fetchSteering(m.sessionCtx, m.controller))
		}
		if m.can(session.CapEscalations) {
			cmds = append(cmds, fetchEscalations(m.sessionCtx, m.controller))
		}
		return m, tea.Batch(cmds...)

	case TickMsg:
		cmds := []tea.Cmd{tick()}
		if m.host != nil {
			cmds = append(cmds, fetchSessions(m.ctx, m.host))
		}
		if !m.attached() {
			return m, tea.Batch(cmds...)
		}
		cmds = append(cmds, fetchSnapshot(m.sessionCtx, m.controller))
		if m.quitting && m.data.snapshot.State == control.RunStatePaused {
			// The pause-then-exit path: leave once research is safely at
			// a boundary rather than killing it mid-invocation.
			return m, tea.Quit
		}
		switch m.view {
		case viewMemory:
			if m.can(session.CapMemory) {
				cmds = append(cmds, fetchMemory(m.sessionCtx, m.controller))
			}
		case viewEscalations:
			if m.can(session.CapEscalations) {
				cmds = append(cmds, fetchEscalations(m.sessionCtx, m.controller))
			}
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
		return tea.Batch(fetchEscalations(m.sessionCtx, m.controller), fetchSnapshot(m.sessionCtx, m.controller))

	case invocation.EventSteeringReceived, invocation.EventSteeringApplied:
		return fetchSteering(m.sessionCtx, m.controller)

	case invocation.EventMemoryWritten, invocation.EventControlReturnedToAri:
		return fetchSnapshot(m.sessionCtx, m.controller)

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
	case modeSteering, modeCommand, modeDenyReason, modeNewTask:
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
	// The switcher works with no session attached; everything else needs
	// one, so an empty harness answers with a hint instead of a crash.
	if !m.attached() && key != "0" && key != "?" && key != "q" && key != "ctrl+c" &&
		key != "esc" && key != "n" && key != "/" {
		if m.view == viewSessions {
			return m, m.handleSessionsKey(key)
		}
		m.view = viewSessions
		m.setAlert("no session attached — select one and press enter")
		return m, nil
	}

	if m.view == viewSessions {
		if cmd, handled := m.sessionsViewKey(key); handled {
			return m, cmd
		}
	}

	switch key {
	case "0":
		m.view = viewSessions
		if m.host != nil {
			return m, fetchSessions(m.ctx, m.host)
		}
		return m, nil
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
		if !m.requireCap(session.CapEscalations, "escalations") {
			return m, nil
		}
		m.view = viewEscalations
		return m, fetchEscalations(m.sessionCtx, m.controller)
	case "5":
		if !m.requireCap(session.CapMemory, "memory") {
			return m, nil
		}
		m.view = viewMemory
		return m, fetchMemory(m.sessionCtx, m.controller)

	case "tab":
		m.focus = (m.focus + 1) % m.paneCount()
		return m, nil
	case "shift+tab":
		m.focus = (m.focus - 1 + m.paneCount()) % m.paneCount()
		return m, nil

	case "s":
		if !m.requireCap(session.CapSteer, "steering") {
			return m, nil
		}
		return m, m.startInput(modeSteering, "steer > ", "instruction for the next research turn")
	case "/":
		return m, m.startInput(modeCommand, "> ", "/new … | /steer … | /pause | /resume | /sessions")

	case "p":
		if !m.requireCap(session.CapPauseResume, "pause") {
			return m, nil
		}
		return m, sendCommand(m.sessionCtx, m.controller, control.PauseCommand{})
	case "r":
		if !m.requireCap(session.CapPauseResume, "resume") {
			return m, nil
		}
		return m, sendCommand(m.sessionCtx, m.controller, control.ResumeCommand{})

	case "a":
		esc, ok := m.selectedEscalation()
		if !ok || esc.Decision != control.DecisionPending {
			m.setAlert("no escalation is awaiting a decision")
			return m, nil
		}
		return m, sendCommand(m.sessionCtx, m.controller, control.ApproveEscalationCommand{EscalationID: esc.ID})

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
	case viewSessions:
		return m.sessionsScreen.handleKey(key, &m.data)
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
	if !m.attached() {
		return m, tea.Quit
	}
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
		return m, sendCommand(m.sessionCtx, m.controller, control.PauseCommand{})

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

// requireCap reports whether the active session supports a capability, and
// explains the refusal when it does not. Saying "this session has no
// memory" is far better than a key that silently does nothing.
func (m *Model) requireCap(c session.Capability, surface string) bool {
	if m.can(c) {
		return true
	}
	m.setAlert("this session has no %s", surface)
	return false
}

// sessionsViewKey handles the keys that only mean something on the
// switcher. It reports whether it consumed the key.
func (m *Model) sessionsViewKey(key string) (tea.Cmd, bool) {
	switch key {
	case "n":
		// Bound here rather than globally: the agents screen already uses
		// n to cycle agents, and a key that means two things depending on
		// which pane has focus is worse than one that means one thing in
		// one place. /new works from anywhere.
		if !m.canStart() {
			m.setAlert("this harness cannot start new work")
			return nil, true
		}
		return m.startInput(modeNewTask, "task > ", "what should the agent do?"), true

	case "enter":
		desc, ok := m.sessionsScreen.selected(&m.data)
		if !ok {
			m.setAlert("no session selected")
			return nil, true
		}
		if desc.ID == m.activeID {
			m.view = viewResearch
			return nil, true
		}
		return m.attach(desc.ID), true
	}
	return nil, false
}

// handleSessionsKey is the reduced key set available with no session
// attached: navigate the list and attach.
func (m *Model) handleSessionsKey(key string) tea.Cmd {
	if cmd, handled := m.sessionsViewKey(key); handled {
		return cmd
	}
	return m.sessionsScreen.handleKey(key, &m.data)
}

// applySessionChange folds one host change into the session list, so the
// switcher stays live without re-listing on every mutation.
func (m *Model) applySessionChange(change session.Change) {
	switch change.Kind {
	case session.ChangeRemoved:
		for i, d := range m.data.sessions {
			if d.ID == change.Descriptor.ID {
				m.data.sessions = append(m.data.sessions[:i], m.data.sessions[i+1:]...)
				break
			}
		}
	default:
		found := false
		for i, d := range m.data.sessions {
			if d.ID == change.Descriptor.ID {
				m.data.sessions[i] = change.Descriptor
				found = true
				break
			}
		}
		if !found {
			m.data.sessions = append(m.data.sessions, change.Descriptor)
		}
	}
	m.sessionsScreen.clamp(&m.data)
}

// reactToSessionChange responds to a change that affects the session the
// user is currently attached to.
func (m *Model) reactToSessionChange(change session.Change) tea.Cmd {
	if change.Kind == session.ChangeRemoved && change.Descriptor.ID == m.activeID {
		// The attached session went away underneath the user. Detach
		// rather than keep rendering a controller the host has disowned.
		if m.sessionCancel != nil {
			m.sessionCancel()
			m.sessionCancel = nil
		}
		m.controller = nil
		m.activeID = ""
		m.caps = nil
		m.view = viewSessions
		m.setAlert("session %s ended", change.Descriptor.Title)
	}
	return nil
}
