package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/haha-systems/ariadne/internal/control"
)

// researchScreen is the default view: the whole state of the programme at a
// glance, so the common case needs no navigation at all.
type researchScreen struct{}

// handleKey exists so every screen answers the same navigation keys; the
// overview is a fixed summary with nothing to scroll through.
func (s *researchScreen) handleKey(_ string, _ *data) tea.Cmd { return nil }

func (s *researchScreen) view(d *data, width, height int, focus int) string {
	half := height / 2
	if half < 3 {
		half = 3
	}

	// Narrow terminals stack every pane; wide ones use two columns.
	if width < mediumWidth {
		return clampLines(strings.Join([]string{
			pane("AGENTS", s.agentLines(d, width-4), width, half/2+1, focus == 0),
			pane("ACTIVE AGENT", s.activeAgentLines(d, width-4), width, half/2+1, focus == 1),
			pane("RESEARCH STATE", s.stateLines(d, width-4), width, half/2+1, focus == 2),
			pane("EVENT LOG", s.eventLines(d, width-4, half/2), width, half/2+1, focus == 3),
		}, "\n"), height, width)
	}

	leftWidth := width / 3
	if width >= wideWidth {
		leftWidth = width * 2 / 5
	}
	rightWidth := width - leftWidth

	top := lipgloss.JoinHorizontal(lipgloss.Top,
		pane("AGENTS", s.agentLines(d, leftWidth-4), leftWidth, half, focus == 0),
		pane("ACTIVE AGENT", s.activeAgentLines(d, rightWidth-4), rightWidth, half, focus == 1),
	)
	bottom := lipgloss.JoinHorizontal(lipgloss.Top,
		pane("RESEARCH STATE", s.stateLines(d, leftWidth-4), leftWidth, height-half, focus == 2),
		pane("EVENT LOG", s.eventLines(d, rightWidth-4, height-half-3), rightWidth, height-half, focus == 3),
	)
	return clampLines(lipgloss.JoinVertical(lipgloss.Left, top, bottom), height, width)
}

func (s *researchScreen) agentLines(d *data, width int) string {
	if len(d.snapshot.Agents) == 0 {
		return styleMuted.Render("no agents configured")
	}
	var b strings.Builder
	for _, agent := range d.snapshot.Agents {
		marker := "  "
		if agent.Name == d.snapshot.ActiveAgent {
			marker = "> "
		}
		fmt.Fprintf(&b, "%s%s\n", marker, truncateLine(agent.Name, width-2))
		// The state word always appears; colour only reinforces it.
		state := agentStateStyle(agent.State).Render(string(agent.State))
		if elapsed := agent.Elapsed(); elapsed > 0 {
			state += styleMuted.Render(fmt.Sprintf("  %ds", int(elapsed.Seconds())))
		}
		fmt.Fprintf(&b, "    %s\n", state)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *researchScreen) activeAgentLines(d *data, width int) string {
	agent := d.snapshot.ActiveAgent
	if agent == "" {
		// Show the most recent result rather than an empty pane: between
		// invocations, what just happened is the useful thing.
		if d.snapshot.LatestResult != "" {
			return styleMuted.Render("no agent running. latest result:") + "\n" +
				strings.Join(wrap(d.snapshot.LatestResult, width), "\n")
		}
		return styleMuted.Render("no agent running")
	}

	var b strings.Builder
	for _, a := range d.snapshot.Agents {
		if a.Name != agent {
			continue
		}
		if a.CurrentTask != "" {
			b.WriteString(strings.Join(wrap(a.CurrentTask, width), "\n") + "\n\n")
		}
	}

	// The tail of this agent's live output.
	var tail []string
	for _, entry := range d.logs {
		if entry.Agent == agent {
			tail = append(tail, truncateLine(entry.Text, width))
		}
	}
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
	b.WriteString(strings.Join(tail, "\n"))
	return strings.TrimRight(b.String(), "\n")
}

func (s *researchScreen) stateLines(d *data, width int) string {
	snap := d.snapshot
	lines := []string{
		fmt.Sprintf("Active hypotheses: %d", snap.ActiveHypotheses),
		fmt.Sprintf("Rejected:          %d", snap.RejectedCount),
		fmt.Sprintf("Open questions:    %d", snap.OpenQuestionCount),
		fmt.Sprintf("Known confounds:   %d", snap.ConfoundCount),
		fmt.Sprintf("Pending steering:  %d", snap.PendingSteering),
	}
	if snap.CurrentExperiment != "" {
		lines = append(lines, "", "Current experiment:")
		lines = append(lines, wrap(snap.CurrentExperiment, width)...)
	}
	if snap.Frontier != "" {
		lines = append(lines, "", styleSectionHead.Render("FRONTIER"))
		lines = append(lines, wrap(snap.Frontier, width)...)
	}
	return strings.Join(lines, "\n")
}

func (s *researchScreen) eventLines(d *data, width, rows int) string {
	if len(d.events) == 0 {
		return styleMuted.Render("no events yet")
	}
	if rows < 1 {
		rows = 1
	}
	events := d.events
	if len(events) > rows {
		events = events[len(events)-rows:]
	}
	var out []string
	for _, ev := range events {
		out = append(out, truncateLine(formatEventLine(ev), width))
	}
	return strings.Join(out, "\n")
}

// formatEventLine renders one event as plain, copyable text: a timestamp,
// the event name, and its most salient field.
func formatEventLine(ev control.Event) string {
	ts := ev.Time.Local().Format("15:04:05")
	detail := eventDetail(ev)
	if detail == "" {
		return ts + "  " + ev.Name
	}
	return ts + "  " + ev.Name + "  " + detail
}

// eventDetail picks the one field worth showing inline for each event kind.
func eventDetail(ev control.Event) string {
	switch ev.Name {
	case "research_cycle_started", "ari_invocation_started":
		if c, ok := ev.Number("cycle"); ok {
			return fmt.Sprintf("cycle %d", int(c))
		}
	case "ari_invocation_completed", "astra_invocation_completed":
		return ev.String("status")
	case "escalation_scored":
		if score, ok := ev.Number("score"); ok {
			return fmt.Sprintf("score %.2f", score)
		}
	case "escalation_requested":
		return ev.String("question")
	case "escalation_approved", "escalation_denied":
		return ev.String("reason")
	case "steering_received", "steering_applied":
		return ev.String("message")
	case "memory_write_failed", "invocation_failed":
		return ev.String("error")
	case "ari_identity":
		return ev.String("agent")
	}
	for _, key := range []string{"reason", "status", "stage", "message"} {
		if v := ev.String(key); v != "" {
			return v
		}
	}
	return ""
}
