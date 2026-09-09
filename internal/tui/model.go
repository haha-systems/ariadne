package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/haha-systems/ariadne/internal/control"
	"github.com/haha-systems/ariadne/internal/session"
)

// view identifies a top-level screen.
type view int

const (
	viewResearch view = iota
	viewAgents
	viewTimeline
	viewEscalations
	viewMemory
	// viewSessions is the switcher. It is last in the iota so the
	// numbered shortcuts for the per-session screens keep their meaning.
	viewSessions
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
	case viewSessions:
		return "Sessions"
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
	// modeNewTask is the prompt for starting ad-hoc work from the
	// interface, offered only when the host can actually start it.
	modeNewTask
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

	// sessions is the harness's view of every session the host knows
	// about, refreshed from the host's change stream.
	sessions []session.Descriptor

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
	controller session.Controller

	// host is the set of sessions the harness can switch between. A
	// command that attached to exactly one session supplies a single-entry
	// host, so there is only ever one code path for opening a session.
	host session.Host

	// activeID is the session the per-session screens are showing.
	activeID string
	// caps is the active session's capability set. Screens and keys are
	// gated on it rather than on the session's kind, so a new workflow
	// never means editing a switch in the interface.
	caps session.Capabilities

	// sessionCancel tears down the previous session's subscriptions when
	// the user switches. Without it, switching would leak a goroutine and
	// a channel per visited session.
	sessionCancel context.CancelFunc

	width, height int

	view  view
	mode  mode
	data  data
	focus int

	sessionsScreen sessionsScreen
	research       researchScreen
	agents         agentsScreen
	timeline       timelineScreen
	escalations    escalationsScreen
	memory         memoryScreen

	input textinput.Model

	// alert is the most recent recoverable error. A runtime failure is
	// surfaced here and in the timeline; it never takes the interface down.
	alert    string
	quitting bool

	// denyTarget is the escalation a deny-reason prompt applies to.
	denyTarget string

	// The live channels are retained so each received message can re-arm
	// the next receive.
	eventCh   <-chan control.Event
	logCh     <-chan control.AgentLogEntry
	sessionCh <-chan session.Change

	// sessionCtx scopes the active session's reads and subscriptions.
	sessionCtx context.Context
}

// New constructs a Model over a single research controller. It is the
// pre-harness entry point, preserved because attaching to one known session
// is still the common case from the CLI.
func New(ctx context.Context, controller control.ResearchController) *Model {
	desc := session.Descriptor{ID: "research", Kind: session.KindResearch, Title: "research"}
	adapted, err := session.Adapt(desc, session.ResearchCapabilities(), controller)
	if err != nil {
		// Adapt only fails on a nil controller or empty ID, neither of
		// which this call site can produce.
		panic("tui: adapting controller: " + err.Error())
	}

	m := newModel(ctx)
	m.controller = adapted
	m.activeID = desc.ID
	m.caps = adapted.Capabilities()

	if host, hostErr := session.Single(desc, adapted); hostErr == nil {
		m.host = host
	}
	return m
}

// NewHarness constructs a Model over a host of many sessions. It opens on
// the switcher, because with more than one session running the first
// question is always which one you are looking at.
func NewHarness(ctx context.Context, host session.Host) *Model {
	m := newModel(ctx)
	m.host = host
	m.view = viewSessions
	return m
}

func newModel(ctx context.Context) *Model {
	input := textinput.New()
	input.Prompt = "steer > "
	input.SetWidth(60)

	return &Model{
		ctx:    ctx,
		view:   viewResearch,
		input:  input,
		width:  80,
		height: 24,
		data:   data{live: true},
		agents: newAgentsScreen(),
	}
}

// attached reports whether a session is currently open. Every per-session
// read and command checks it, so the harness sitting on an empty switcher
// cannot dereference a controller that is not there.
func (m *Model) attached() bool { return m.controller != nil }

// can reports whether the active session supports a capability.
func (m *Model) can(c session.Capability) bool { return m.caps.Has(c) }

// canStart reports whether the host can begin new work. A host that only
// observes must not be offered a key that would spend an invocation.
func (m *Model) canStart() bool {
	if m.host == nil {
		return false
	}
	_, ok := session.AsStarter(m.host)
	return ok
}

// Init subscribes to the live streams and loads replay context. Both
// subscriptions are established before the first snapshot so no event
// occurring during startup is missed.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick()}

	if m.host != nil {
		cmds = append(cmds, fetchSessions(m.ctx, m.host))
		if ch, err := m.host.Watch(m.ctx); err == nil {
			cmds = append(cmds, waitForSessionChange(ch))
			m.sessionCh = ch
		}
	}
	if m.attached() {
		cmds = append(cmds, m.attachCmds()...)
	}
	return tea.Batch(cmds...)
}

// attachCmds are the reads and subscriptions for the active session. They
// are split out of Init because switching sessions has to do exactly the
// same work again against a different controller.
//
// Optional reads are issued only when the session declares the capability,
// so switching to an issue run does not fill the alert area with
// "memory unavailable" for a session that was never going to have memory.
func (m *Model) attachCmds() []tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	m.sessionCancel = cancel
	m.sessionCtx = ctx

	cmds := []tea.Cmd{
		fetchHistory(ctx, m.controller, 500),
		fetchSnapshot(ctx, m.controller),
	}
	if m.can(session.CapEscalations) {
		cmds = append(cmds, fetchEscalations(ctx, m.controller))
	}
	if m.can(session.CapMemory) {
		cmds = append(cmds, fetchMemory(ctx, m.controller))
	}
	if m.can(session.CapSteer) {
		cmds = append(cmds, fetchSteering(ctx, m.controller))
	}

	if ch, err := m.controller.Subscribe(ctx); err == nil {
		cmds = append(cmds, waitForEvent(ch))
		m.eventCh = ch
	} else {
		m.data.live = false
		m.alert = "live events unavailable: " + err.Error()
	}
	if ch, err := m.controller.SubscribeLogs(ctx); err == nil {
		cmds = append(cmds, waitForLog(ch))
		m.logCh = ch
	}
	return cmds
}

// attach switches the per-session screens to a different session. The
// previous session's subscriptions are cancelled and its buffered data
// dropped, so the timeline and log panes never blend two sessions together.
func (m *Model) attach(id string) tea.Cmd {
	if m.host == nil {
		return nil
	}
	controller, err := m.host.Open(m.ctx, id)
	if err != nil {
		m.setAlert("cannot open session %s: %v", id, err)
		return nil
	}

	if m.sessionCancel != nil {
		m.sessionCancel()
		m.sessionCancel = nil
	}

	m.controller = controller
	m.activeID = id
	m.caps = controller.Capabilities()

	sessions := m.data.sessions
	m.data = data{live: true, sessions: sessions}
	m.eventCh, m.logCh = nil, nil
	m.agents = newAgentsScreen()
	m.alert = ""

	// Land on the session's own status view: the user picked a session to
	// look at it, not to stay on the list.
	m.view = viewResearch
	m.focus = 0

	return tea.Batch(m.attachCmds()...)
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
	m.resizeInput()
	return m.input.Focus()
}

// inputHint is the trailing help shown beside a prompt. Its width is
// reserved when sizing the text box, so the hint is never squeezed to an
// ellipsis by a long input.
const inputHint = "enter send  esc cancel"

// resizeInput sizes the text box to the space left after its prompt and the
// trailing hint.
func (m *Model) resizeInput() {
	reserved := lipgloss.Width(m.input.Prompt) + lipgloss.Width(inputHint) + 4
	m.input.SetWidth(maxInt(12, m.width-reserved))
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
		return sendCommand(m.sessionCtx, m.controller, control.SteerCommand{Message: text})

	case modeDenyReason:
		return sendCommand(m.sessionCtx, m.controller, control.DenyEscalationCommand{
			EscalationID: target, Reason: text,
		})

	case modeNewTask:
		if text == "" {
			return nil
		}
		return startTask(m.ctx, m.host, text)

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
		return sendCommand(m.sessionCtx, m.controller, control.SteerCommand{Message: rest})
	case "pause":
		return sendCommand(m.sessionCtx, m.controller, control.PauseCommand{})
	case "resume":
		return sendCommand(m.ctx, m.controller, control.ResumeCommand{})
	case "status":
		m.view = viewResearch
		return fetchSnapshot(m.sessionCtx, m.controller)
	case "new", "task":
		if rest == "" {
			m.setAlert("usage: /new <what the agent should do>")
			return nil
		}
		return startTask(m.ctx, m.host, rest)
	case "sessions":
		m.view = viewSessions
		return fetchSessions(m.ctx, m.host)
	default:
		// Plain text with no recognized verb is a steering message, which
		// is what a user typing into a research tool almost always means.
		// With nothing attached, or a session that cannot be steered, the
		// same text is far more likely to be a task.
		if !m.attached() || !m.can(session.CapSteer) {
			return startTask(m.ctx, m.host, text)
		}
		return sendCommand(m.sessionCtx, m.controller, control.SteerCommand{Message: text})
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
