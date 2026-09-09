package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/haha-systems/ariadne/internal/control"
)

// Colour is an accent here, never the message. Every state that colour
// distinguishes is also spelled out in words — ACTIVE, IDLE, PAUSED,
// ESCALATION PENDING — so the interface stays fully readable in a
// monochrome terminal, over a screen reader, or in copied text.
var (
	colFrame  = lipgloss.Color("240")
	colMuted  = lipgloss.Color("245")
	colTitle  = lipgloss.Color("111")
	colActive = lipgloss.Color("42")
	colWarn   = lipgloss.Color("214")
	colErr    = lipgloss.Color("203")
	colScarce = lipgloss.Color("177")
)

var (
	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(colTitle)
	styleMuted    = lipgloss.NewStyle().Foreground(colMuted)
	styleActive   = lipgloss.NewStyle().Foreground(colActive)
	styleWarn     = lipgloss.NewStyle().Foreground(colWarn)
	styleErr      = lipgloss.NewStyle().Foreground(colErr)
	styleScarce   = lipgloss.NewStyle().Foreground(colScarce)
	styleSelected = lipgloss.NewStyle().Bold(true)

	styleSectionHead = lipgloss.NewStyle().Bold(true).Foreground(colMuted)
)

// pane draws a titled box. The title is plain text inside the frame rather
// than embedded in the border, so copying the pane's contents yields
// readable text instead of box-drawing debris.
func pane(title string, body string, width, height int, focused bool) string {
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colFrame).
		Padding(0, 1)
	if focused {
		border = border.BorderForeground(colTitle)
	}

	// lipgloss Width/Height set the *total* rendered size, borders and
	// padding included. The usable content area is therefore 4 columns and
	// 2 rows smaller — get this wrong and the content lipgloss is handed is
	// wider than the box, so it soft-wraps and each pane silently grows
	// taller than the layout budgeted for.
	inner := width - 4
	if inner < 4 {
		inner = 4
	}

	head := title
	if focused {
		// A focus marker that survives monochrome rendering.
		head = "* " + title
	}

	content := styleSectionHead.Render(truncateLine(head, inner))
	if body != "" {
		content += "\n" + body
	}

	style := border.Width(width)
	if height > 0 {
		bodyHeight := height - 2
		if bodyHeight < 1 {
			bodyHeight = 1
		}
		style = style.Height(height)
		content = clampLines(content, bodyHeight, inner)
	}
	return style.Render(content)
}

// statusStyle picks an accent for a run state. The caller always renders
// the state's word as well.
func statusStyle(state control.RunState) lipgloss.Style {
	switch state {
	case control.RunStateRunning:
		return styleActive
	case control.RunStatePaused, control.RunStatePauseRequested:
		return styleWarn
	case control.RunStateFailed:
		return styleErr
	default:
		return styleMuted
	}
}

func agentStateStyle(state control.AgentState) lipgloss.Style {
	switch state {
	case control.AgentWorking:
		return styleActive
	case control.AgentWaiting:
		return styleWarn
	case control.AgentFailed, control.AgentBlocked:
		return styleErr
	default:
		return styleMuted
	}
}

// clampLines trims content to at most n lines, truncating each to width.
// Every pane clamps its own body so a long value can never push the layout
// off screen.
func clampLines(content string, n, width int) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, n)
	for i, line := range lines {
		if i >= n {
			break
		}
		out = append(out, truncateLine(line, width))
	}
	return strings.Join(out, "\n")
}

// truncateLine shortens a line to width display cells, accounting for the
// ANSI escapes styling may have introduced.
func truncateLine(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}

	// Trim by runes until it fits; lipgloss.Width accounts for escapes and
	// wide runes, which a byte slice would not.
	runes := []rune(s)
	for len(runes) > 0 {
		candidate := string(runes) + "…"
		if lipgloss.Width(candidate) <= width {
			return candidate
		}
		runes = runes[:len(runes)-1]
	}
	return "…"
}

// wrap breaks text into lines of at most width, on word boundaries where
// possible. Body text is wrapped rather than truncated so a frontier
// summary stays readable and copyable.
func wrap(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range words {
			switch {
			case line == "":
				line = word
			case lipgloss.Width(line+" "+word) <= width:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
