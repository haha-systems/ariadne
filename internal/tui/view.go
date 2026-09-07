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

	project := s.Project
	if project == "" {
		project = "(no project)"
	}

	active := s.ActiveAgent
	if active == "" {
		active = "-"
	}

	fields := []string{
		styleTitle.Render(project),
		statusStyle(s.State).Render(s.State.Display()),
		fmt.Sprintf("cycle %d", s.Cycle),
		"agent " + active,
		"QAC:" + orDash(s.QAC.Mode),
		m.renderBudget(),
	}
	if s.PendingSteering > 0 {
		fields = append(fields, styleWarn.Render(fmt.Sprintf("steering %d pending", s.PendingSteering)))
	}
	if s.QAC.PendingEscalation != nil && s.QAC.PendingEscalation.Decision == control.DecisionPending {
		fields = append(fields, styleScarce.Render("ESCALATION PENDING"))
	}
	if !m.data.live {
		fields = append(fields, styleMuted.Render("events: not live"))
	}
	if m.width >= wideWidth && s.MemoryBackend != "" {
		fields = append(fields, styleMuted.Render("memory:"+s.MemoryBackend))
	}

	line := strings.Join(fields, styleMuted.Render(" | "))
	return truncateLine(line, m.width)
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
	case modeSteering, modeCommand, modeDenyReason:
		return truncateLine(m.input.View()+"   "+styleMuted.Render("enter send  esc cancel"), m.width)
	case modeHelp:
		keys = "any key  close help"
	case modeQuitConfirm:
		keys = "p pause and exit   d detach and leave running   esc cancel"
	default:
		if m.width >= wideWidth {
			keys = "1-5 views  tab focus  j/k move  enter select  s steer  / command  a approve  d deny  p pause  r resume  ? help  q quit"
		} else if m.width >= mediumWidth {
			keys = "1-5 views  tab  j/k  s steer  a/d qac  p/r pause  ? help  q quit"
		} else {
			keys = "1-5 views  s steer  ? help  q quit"
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
		"  1            Research overview",
		"  2            Agents and live logs",
		"  3            Timeline of runtime events",
		"  4            QAC escalations",
		"  5            Research memory",
		"",
		"  tab / shift-tab   Move focus between panes",
		"  j / k, up / down  Move within a list",
		"  enter             Select; in Agents, follow the chosen agent",
		"  esc               Back, or dismiss an alert",
		"",
		"  s            Steer: send an instruction to the next research turn",
		"  /            Command: /steer … /pause /resume /status",
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
		styleTitle.Render("  Research is still running."),
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
