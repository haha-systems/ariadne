package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/haha-systems/ariadne/internal/control"
)

// Layout breakpoints. Below minWidth/minHeight the interface renders a
// readable warning rather than a broken layout — a cramped terminal should
// produce a sentence, not garbage.
const (
	wideWidth   = 100
	mediumWidth = 72
	minWidth    = 40
	minHeight   = 10
)

// View renders the whole interface.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *Model) render() string {
	if m.width < minWidth || m.height < minHeight {
		return fmt.Sprintf(
			"Terminal too small.\n\nAriadne needs at least %dx%d; this one is %dx%d.\nResize the window, or press q to quit.",
			minWidth, minHeight, m.width, m.height)
	}

	header := m.renderHeader()
	footer := m.renderFooter()
	alert := m.renderAlert()

	chrome := lipgloss.Height(header) + lipgloss.Height(footer)
	if alert != "" {
		chrome += lipgloss.Height(alert)
	}
	bodyHeight := m.height - chrome
	if bodyHeight < 3 {
		bodyHeight = 3
	}

	body := m.renderBody(bodyHeight)

	parts := []string{header}
	if alert != "" {
		parts = append(parts, alert)
	}
	parts = append(parts, body, footer)
	return strings.Join(parts, "\n")
}

func (m *Model) renderBody(height int) string {
	switch m.mode {
	case modeHelp:
		return m.renderHelp(height)
	case modeQuitConfirm:
		return m.renderQuitConfirm(height)
	}

	switch m.view {
	case viewSessions:
		return m.sessionsScreen.view(&m.data, m.activeID, m.width, height, m.focus)
	case viewAgents:
		return m.agents.view(&m.data, m.width, height, m.focus)
	case viewTimeline:
		return m.timeline.view(&m.data, m.width, height, m.focus)
	case viewEscalations:
		return m.escalations.view(&m.data, m.width, height, m.focus)
	case viewMemory:
		return m.memory.view(&m.data, m.width, height, m.focus)
	default:
		return m.research.view(&m.data, m.width, height, m.focus)
	}
}

// renderHeader is the compact status line every view carries. Each field is
// labelled, so the line reads as text rather than relying on position or
// colour to convey meaning.
func (m *Model) renderHeader() string {
	s := m.data.snapshot

	if !m.attached() {
		// With nothing attached the research fields would all be empty or
		// zero, which reads as a broken run rather than an idle harness.
		return fitHeader([]headerField{
			{styleTitle.Render("ariadne"), 0},
			{fmt.Sprintf("%d sessions", len(m.data.sessions)), 0},
			{styleMuted.Render("no session attached"), 1},
		}, m.width)
	}

	project := s.Project
	if project == "" {
		project = "(no project)"
	}

	active := s.ActiveAgent
	if active == "" {
		active = "-"
	}

	// Each field carries a priority. When the line does not fit, the
	// least important fields are dropped rather than the line being cut —
	// truncating would hide whichever field happened to land last, and
	// that field is often the escalation notice, the single thing a user
	// most needs to see.
	fields := []headerField{
		{styleTitle.Render(project), 0},
		{statusStyle(s.State).Render(s.State.Display()), 0},
		{fmt.Sprintf("cycle %d", s.Cycle), 2},
		{"agent " + active, 3},
		{"QAC:" + orDash(s.QAC.Mode), 4},
		{m.renderBudget(), 3},
	}
	if s.QAC.PendingEscalation != nil && s.QAC.PendingEscalation.Decision == control.DecisionPending {
		fields = append(fields, headerField{styleScarce.Render("ESCALATION PENDING"), 1})
	}
	if s.PendingSteering > 0 {
		fields = append(fields, headerField{styleWarn.Render(fmt.Sprintf("steering %d pending", s.PendingSteering)), 4})
	}
	if !m.data.live {
		fields = append(fields, headerField{styleMuted.Render("events: not live"), 2})
	}
	if s.MemoryBackend != "" {
		fields = append(fields, headerField{styleMuted.Render("memory:" + s.MemoryBackend), 5})
	}
	if n := len(m.data.sessions); n > 1 {
		fields = append(fields, headerField{fmt.Sprintf("%d sessions", n), 1})
	}

	return fitHeader(fields, m.width)
}

// headerField is one status field and how readily it may be dropped. A
// lower priority number means "keep this for longer".
type headerField struct {
	text     string
	priority int
}

// fitHeader assembles the status line, dropping the least important fields
// until it fits. Display order is preserved so the line does not reshuffle
// as the terminal resizes.
func fitHeader(fields []headerField, width int) string {
	const sep = " | "

	for cut := 5; cut >= 0; cut-- {
		var kept []string
		for _, f := range fields {
			if f.priority <= cut {
				kept = append(kept, f.text)
			}
		}
		line := strings.Join(kept, styleMuted.Render(sep))
		if lipgloss.Width(line) <= width {
			return line
		}
	}

	// Even the essentials do not fit; truncate what is left.
	var essential []string
	for _, f := range fields {
		if f.priority == 0 {
			essential = append(essential, f.text)
		}
	}
	return truncateLine(strings.Join(essential, styleMuted.Render(sep)), width)
}

// renderBudget spells out scarce-agent usage, distinguishing an unlimited
// budget from an exhausted one — they are opposite situations.
func (m *Model) renderBudget() string {
	q := m.data.snapshot.QAC
	if !q.CallsRemainingKnown {
		return fmt.Sprintf("Astra %d used (no limit)", q.CallsUsed)
	}
	total := q.CallsUsed + q.CallsRemaining
	text := fmt.Sprintf("Astra %d/%d", q.CallsUsed, total)
	if q.CallsRemaining == 0 {
		return styleWarn.Render(text + " EXHAUSTED")
	}
	return text
}

// renderFooter shows the keys that currently do something. In a modal it
// shows only that modal's keys, so the footer never advertises a key that
// would be swallowed.
func (m *Model) renderFooter() string {
	var keys string
	switch m.mode {
	case modeSteering, modeCommand, modeDenyReason, modeNewTask:
		return truncateLine(m.input.View()+"   "+styleMuted.Render(inputHint), m.width)
	case modeHelp:
		keys = "any key  close help"
	case modeQuitConfirm:
		keys = "p pause and exit   d detach and leave running   esc cancel"
	default:
		if m.width >= wideWidth {
			keys = "0 sessions  1-5 views  tab focus  j/k move  enter select  s steer  / command  a approve  d deny  p pause  r resume  ? help  q quit"
		} else if m.width >= mediumWidth {
			keys = "0 sessions  1-5 views  tab  j/k  s steer  a/d qac  p/r pause  ? help  q quit"
		} else {
			keys = "0 sessions  1-5 views  s steer  ? help  q quit"
		}
		if m.view == viewSessions {
			if m.width >= mediumWidth {
				keys = "j/k move  enter attach  n new task  1-5 session views  ? help  q quit"
			} else {
				keys = "j/k  enter attach  ? help  q quit"
			}
		}
	}
	return styleMuted.Render(truncateLine(keys, m.width))
}

func (m *Model) renderAlert() string {
	if m.alert == "" {
		return ""
	}
	return styleErr.Render(truncateLine("! "+m.alert+"   (esc to dismiss)", m.width))
}

func (m *Model) renderHelp(height int) string {
	lines := []string{
		"KEYS",
		"",
		"  0            Sessions: every run this harness knows about",
		"  1            Research overview",
		"  2            Agents and live logs",
		"  3            Timeline of runtime events",
		"  4            QAC escalations",
		"  5            Research memory",
		"",
		"  tab / shift-tab   Move focus between panes",
		"  j / k, up / down  Move within a list",
		"  enter             Select; on Sessions, attach to the highlighted one",
		"  esc               Back, or dismiss an alert",
		"",
		"  n            New task (on the session list); /new works anywhere",
		"  s            Steer: send an instruction to the next research turn",
		"  /            Command: /new … /steer … /pause /resume /sessions",
		"",
		"  a            Approve the pending escalation (manual QAC only)",
		"  d            Deny it, with an optional reason",
		"",
		"  p            Pause at the next safe boundary",
		"  r            Resume",
		"  q            Quit",
		"",
		"NOTES",
		"",
		"  Every kind of run — a claimed issue, a research programme — is a",
		"  session. Press 0 to switch between them. A session offers only",
		"  the surfaces it actually has: an issue run has no memory or QAC.",
		"",
		"  Pausing never kills a running agent. It stops the loop at the",
		"  next cycle boundary, so work already paid for is not discarded.",
		"",
		"  Steering is part of the research record, not chat: it persists,",
		"  reaches the next agent turn, and is then marked applied.",
		"",
		"  Quitting does not end the research run unless you choose to",
		"  pause it first.",
	}
	return clampLines(strings.Join(lines, "\n"), height, m.width)
}

func (m *Model) renderQuitConfirm(height int) string {
	lines := []string{
		"",
		styleTitle.Render("  This session is still running."),
		"",
		"  p    Pause at the next safe boundary, then exit.",
		"       The current invocation finishes; nothing is discarded.",
		"",
		"  d    Detach and leave it running.",
		"       The run continues without this interface.",
		"",
		"  esc  Cancel and stay here.",
		"",
	}
	return clampLines(strings.Join(lines, "\n"), height, m.width)
}

// columns lays panes out side by side on a wide terminal and stacks them on
// a narrow one, so no fixed terminal size is ever required.
func (m *Model) columns(height int, left, right func(w, h int) string) string {
	if m.width < mediumWidth {
		top := height / 2
		return lipgloss.JoinVertical(lipgloss.Left,
			left(m.width, top),
			right(m.width, height-top),
		)
	}
	leftWidth := m.width / 3
	if m.width >= wideWidth {
		leftWidth = m.width * 2 / 5
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		left(leftWidth, height),
		right(m.width-leftWidth, height),
	)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
