package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/haha-systems/ariadne/internal/control"
)

// agentSelectionAll is the pseudo-agent that shows every stream merged. It
// is opt-in rather than the default: an interleaved Ari/Astra log is
// unreadable exactly when following one agent matters most.
const agentSelectionAll = "all agents"

// agentsScreen lists configured agents and shows the selected one's log.
type agentsScreen struct {
	cursor int
	// follow keeps the log pinned to the newest line. Scrolling up turns it
	// off, so reading back through output is not fought by new arrivals.
	follow bool
	offset int
}

func newAgentsScreen() agentsScreen { return agentsScreen{follow: true} }

// notifyAppended keeps a following log pinned to the tail.
func (s *agentsScreen) notifyAppended() {
	if s.follow {
		s.offset = 0
	}
}

// names is the selectable list: each configured agent, plus "all agents".
func (s *agentsScreen) names(d *data) []string {
	out := make([]string, 0, len(d.snapshot.Agents)+1)
	for _, a := range d.snapshot.Agents {
		out = append(out, a.Name)
	}
	return append(out, agentSelectionAll)
}

func (s *agentsScreen) selectedName(d *data) string {
	names := s.names(d)
	if len(names) == 0 {
		return ""
	}
	if s.cursor >= len(names) {
		s.cursor = len(names) - 1
	}
	return names[s.cursor]
}

func (s *agentsScreen) handleKey(key string, d *data) tea.Cmd {
	names := s.names(d)
	switch key {
	case "j", "down":
		// Down scrolls the log back through history, which turns follow
		// off; the list itself moves with n/p or enter.
		s.offset++
		s.follow = false
	case "k", "up":
		if s.offset > 0 {
			s.offset--
		}
		if s.offset == 0 {
			s.follow = true
		}
	case "n", "tab":
		if len(names) > 0 {
			s.cursor = (s.cursor + 1) % len(names)
			s.offset, s.follow = 0, true
		}
	case "p":
		if len(names) > 0 {
			s.cursor = (s.cursor - 1 + len(names)) % len(names)
			s.offset, s.follow = 0, true
		}
	case "enter":
		s.offset, s.follow = 0, true
	case "f":
		s.follow = !s.follow
		if s.follow {
			s.offset = 0
		}
	}
	return nil
}

func (s *agentsScreen) view(d *data, width, height int, focus int) string {
	listWidth := width / 3
	if listWidth < 24 {
		listWidth = minInt(width, 24)
	}

	if width < mediumWidth {
		top := height / 2
		return clampLines(strings.Join([]string{
			pane("AGENTS", s.listLines(d, listWidth-4), width, top, focus == 0),
			pane(s.logTitle(d), s.logLines(d, width-4, height-top-3), width, height-top, focus == 1),
		}, "\n"), height, width)
	}

	logWidth := width - listWidth
	left := pane("AGENTS", s.listLines(d, listWidth-4), listWidth, height, focus == 0)
	right := pane(s.logTitle(d), s.logLines(d, logWidth-4, height-3), logWidth, height, focus == 1)
	return clampLines(joinRow(left, right), height, width)
}

func (s *agentsScreen) logTitle(d *data) string {
	title := "LOG — " + s.selectedName(d)
	if s.follow {
		return title + " (following)"
	}
	return title + " (paused — k to catch up)"
}

func (s *agentsScreen) listLines(d *data, width int) string {
	var b strings.Builder
	for i, name := range s.names(d) {
		marker := "  "
		if i == s.cursor {
			marker = "> "
		}

		if name == agentSelectionAll {
			fmt.Fprintf(&b, "%s%s\n", marker, truncateLine(name, width-2))
			continue
		}

		agent := findAgent(d, name)
		fmt.Fprintf(&b, "%s%s\n", marker, truncateLine(agent.Name, width-2))

		detail := []string{string(agent.State)}
		if agent.Role != "" {
			detail = append(detail, agent.Role)
		}
		if agent.ScarcityClass != "" {
			detail = append(detail, agent.ScarcityClass)
		}
		fmt.Fprintf(&b, "    %s\n", truncateLine(agentStateStyle(agent.State).Render(strings.Join(detail, " · ")), width-4))

		if agent.Model != "" || agent.Provider != "" {
			fmt.Fprintf(&b, "    %s\n", styleMuted.Render(truncateLine(agent.Provider+" "+agent.Model, width-4)))
		}
		if elapsed := agent.Elapsed(); elapsed > 0 {
			fmt.Fprintf(&b, "    %s\n", styleMuted.Render(fmt.Sprintf("running %ds", int(elapsed.Seconds()))))
		}
		if agent.LastSummary != "" {
			fmt.Fprintf(&b, "    %s\n", styleMuted.Render(truncateLine("last: "+agent.LastSummary, width-4)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// logLines renders the selected agent's output. Historical (replayed) and
// live lines are both shown; the title says whether the view is following.
func (s *agentsScreen) logLines(d *data, width, rows int) string {
	if rows < 1 {
		rows = 1
	}
	selected := s.selectedName(d)

	var lines []string
	for _, entry := range d.logs {
		if selected != agentSelectionAll && entry.Agent != selected {
			continue
		}
		prefix := entry.Time.Local().Format("15:04:05") + " "
		if selected == agentSelectionAll {
			prefix += entry.Agent + " "
		}
		if entry.Stream == control.StreamStderr {
			prefix += "err "
		}
		lines = append(lines, truncateLine(prefix+entry.Text, width))
	}

	if len(lines) == 0 {
		return styleMuted.Render("no output yet")
	}

	end := len(lines) - s.offset
	if end < 1 {
		end = 1
	}
	start := end - rows
	if start < 0 {
		start = 0
	}
	return strings.Join(lines[start:end], "\n")
}

func findAgent(d *data, name string) control.AgentSnapshot {
	for _, a := range d.snapshot.Agents {
		if a.Name == name {
			return a
		}
	}
	return control.AgentSnapshot{Name: name, State: control.AgentIdle}
}

// joinRow places two rendered blocks side by side.
func joinRow(left, right string) string {
	leftLines := strings.Split(left, "\n")
	rightLines := strings.Split(right, "\n")
	n := len(leftLines)
	if len(rightLines) > n {
		n = len(rightLines)
	}

	var b strings.Builder
	for i := 0; i < n; i++ {
		if i < len(leftLines) {
			b.WriteString(leftLines[i])
		}
		if i < len(rightLines) {
			b.WriteString(rightLines[i])
		}
		if i < n-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
