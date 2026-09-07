package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/haha-systems/ariadne/internal/control"
)

// view identifies a top-level screen.
type view int

const (
	viewResearch view = iota
	viewAgents
	viewTimeline
	viewEscalations
	viewMemory
	viewCount
)

func (v view) String() string {
	switch v {
	case viewResearch:
		return "Research"
	case viewAgents:
		return "Agents"
	case viewTimeline:
		return "Timeline"
	case viewEscalations:
		return "Escalations"
	case viewMemory:
		return "Memory"
	default:
		return "?"
	}
}

// mode is the input mode: either navigating, or typing into a prompt.
type mode int

const (
	modeNormal mode = iota
	modeSteering
	modeCommand
	modeDenyReason
	modeHelp
	modeQuitConfirm
)

// data is everything the model knows about the runtime. Screens read it and
// never call the controller themselves, which is what keeps a screen from
// becoming a second source of truth.
type data struct {
	snapshot    control.ResearchSnapshot
	events      []control.Event
	logs        []control.AgentLogEntry
	escalations []control.EscalationSummary
	memory      control.MemoryView
	steering    []control.SteeringRecord

	// live reports whether the event subscription is still open.
	live bool
}

// maxBuffered bounds the in-model history. The durable log holds
// everything; the interface only needs a scrollback.
const maxBuffered = 2000

// Model is the top-level Bubble Tea model. It is deliberately thin: it
// routes messages, owns shared data, and delegates rendering to screens.
type Model struct {
	ctx        context.Context
	controller control.ResearchController

	width, height int

	view  view
	mode  mode
	data  data
	focus int

	research    researchScreen
	agents      agentsScreen
	timeline    timelineScreen
	escalations escalationsScreen
	memory      memoryScreen

	input textinput.Model

	// alert is the most recent recoverable error. A runtime failure is
	// surfaced here and in the timeline; it never takes the interface down.
	alert    string
	quitting bool

	// denyTarget is the escalation a deny-reason prompt applies to.
	denyTarget string

	// The live channels are retained so each received message can re-arm
	// the next receive.
	eventCh <-chan control.Event
	logCh   <-chan control.AgentLogEntry
}

// New constructs a Model over a controller.
func New(ctx context.Context, controller control.ResearchController) *Model {
	input := textinput.New()
	input.Prompt = "steer > "
	input.SetWidth(60)

	return &Model{
		ctx:        ctx,
		controller: controller,
		view:       viewResearch,
		input:      input,
		width:      80,
		height:     24,
		data:       data{live: true},
		agents:     newAgentsScreen(),
	}
}

// Init subscribes to the live streams and loads replay context. Both
// subscriptions are established before the first snapshot so no event
// occurring during startup is missed.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		fetchHistory(m.ctx, m.controller, 500),
		fetchSnapshot(m.ctx, m.controller),
		fetchEscalations(m.ctx, m.controller),
		fetchMemory(m.ctx, m.controller),
		fetchSteering(m.ctx, m.controller),
		tick(),
	}

	if ch, err := m.controller.Subscribe(m.ctx); err == nil {
		cmds = append(cmds, waitForEvent(ch))
		m.eventCh = ch
	} else {
		m.data.live = false
		m.alert = "live events unavailable: " + err.Error()
	}
	if ch, err := m.controller.SubscribeLogs(m.ctx); err == nil {
		cmds = append(cmds, waitForLog(ch))
		m.logCh = ch
	}
	return tea.Batch(cmds...)
}

// setAlert records a recoverable error for the alert area.
func (m *Model) setAlert(format string, args ...any) {
	m.alert = fmt.Sprintf(format, args...)
}

// appendEvent buffers an event for the timeline.
func (m *Model) appendEvent(ev control.Event) {
	m.data.events = append(m.data.events, ev)
	if len(m.data.events) > maxBuffered {
		m.data.events = m.data.events[len(m.data.events)-maxBuffered:]
	}
	m.timeline.notifyAppended(len(m.data.events))
}

func (m *Model) appendLog(entry control.AgentLogEntry) {
	m.data.logs = append(m.data.logs, entry)
	if len(m.data.logs) > maxBuffered {
		m.data.logs = m.data.logs[len(m.data.logs)-maxBuffered:]
	}
	m.agents.notifyAppended()
}

// selectedEscalation returns the escalation the escalations screen has
// selected, or the pending one when the user is elsewhere — so `a` and `d`
// do the obvious thing from any view.
func (m *Model) selectedEscalation() (control.EscalationSummary, bool) {
	if m.view == viewEscalations {
		if esc, ok := m.escalations.selected(&m.data); ok {
			return esc, true
		}
	}
	if p := m.data.snapshot.QAC.PendingEscalation; p != nil {
		return *p, true
	}
	return control.EscalationSummary{}, false
}

// startInput switches to a text-entry mode.
func (m *Model) startInput(newMode mode, prompt, placeholder string) tea.Cmd {
	m.mode = newMode
	m.input.Reset()
	m.input.Prompt = prompt
	m.input.Placeholder = placeholder
	m.input.SetWidth(maxInt(20, m.width-len(prompt)-6))
	return m.input.Focus()
}

func (m *Model) endInput() {
	m.mode = modeNormal
	m.input.Blur()
	m.input.Reset()
}

// submitInput turns typed text into a command.
func (m *Model) submitInput() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	mode := m.mode
	target := m.denyTarget
	m.endInput()

	switch mode {
	case modeSteering:
		if text == "" {
			return nil
		}
		return sendCommand(m.ctx, m.controller, control.SteerCommand{Message: text})

	case modeDenyReason:
		return sendCommand(m.ctx, m.controller, control.DenyEscalationCommand{
			EscalationID: target, Reason: text,
		})

	case modeCommand:
		return m.runSlashCommand(text)
	}
	return nil
}

// runSlashCommand parses the small command language. It is deliberately
// tiny: anything richer belongs in the CLI, not in a text box.
func (m *Model) runSlashCommand(text string) tea.Cmd {
	text = strings.TrimSpace(strings.TrimPrefix(text, "/"))
	if text == "" {
		return nil
	}

	verb, rest, _ := strings.Cut(text, " ")
	rest = strings.TrimSpace(rest)

	switch strings.ToLower(verb) {
	case "steer":
		if rest == "" {
			m.setAlert("usage: /steer <instruction>")
			return nil
		}
		return sendCommand(m.ctx, m.controller, control.SteerCommand{Message: rest})
	case "pause":
		return sendCommand(m.ctx, m.controller, control.PauseCommand{})
	case "resume":
		return sendCommand(m.ctx, m.controller, control.ResumeCommand{})
	case "status":
		m.view = viewResearch
		return fetchSnapshot(m.ctx, m.controller)
	default:
		// Plain text with no recognized verb is a steering message, which
		// is what a user typing into a research tool almost always means.
		return sendCommand(m.ctx, m.controller, control.SteerCommand{Message: text})
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
