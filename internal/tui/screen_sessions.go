package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/haha-systems/ariadne/internal/session"
)

// sessionsScreen is the switcher: every session the host knows about, in
// one list, regardless of which workflow produced it.
//
// It is the screen that makes Ariadne one harness rather than two tools. It
// renders a session's kind, but it never branches on it — a row is built
// from the Descriptor alone, so a workflow added later appears here with no
// change to this file.
type sessionsScreen struct {
	cursor int
}

// selected returns the highlighted session.
func (s *sessionsScreen) selected(d *data) (session.Descriptor, bool) {
	if s.cursor < 0 || s.cursor >= len(d.sessions) {
		return session.Descriptor{}, false
	}
	return d.sessions[s.cursor], true
}

// clamp keeps the cursor inside the list as sessions come and go, so a run
// finishing underneath the user cannot leave the cursor pointing off the
// end of the list.
func (s *sessionsScreen) clamp(d *data) {
	if s.cursor >= len(d.sessions) {
		s.cursor = maxInt(0, len(d.sessions)-1)
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

func (s *sessionsScreen) handleKey(key string, d *data) tea.Cmd {
	switch key {
	case "j", "down":
		if s.cursor < len(d.sessions)-1 {
			s.cursor++
		}
	case "k", "up":
		if s.cursor > 0 {
			s.cursor--
		}
	case "g", "home":
		s.cursor = 0
	case "G", "end":
		s.cursor = maxInt(0, len(d.sessions)-1)
	}
	return nil
}

func (s *sessionsScreen) view(d *data, activeID string, width, height int, focus int) string {
	if width < mediumWidth {
		return clampLines(
			pane("SESSIONS", s.listLines(d, activeID, width-4, height-3), width, height, focus == 0),
			height, width)
	}

	listWidth := width * 3 / 5
	left := pane("SESSIONS", s.listLines(d, activeID, listWidth-4, height-3), listWidth, height, focus == 0)
	right := pane("SESSION DETAIL", s.detailLines(d, width-listWidth-4), width-listWidth, height, focus == 1)
	return clampLines(joinRow(left, right), height, width)
}

func (s *sessionsScreen) listLines(d *data, activeID string, width, rows int) string {
	if len(d.sessions) == 0 {
		return styleMuted.Render("no sessions yet — press n to start one")
	}
	if rows < 1 {
		rows = 1
	}
	s.clamp(d)

	start := s.cursor - rows/2
	if start < 0 {
		start = 0
	}
	end := minInt(len(d.sessions), start+rows)
	start = maxInt(0, end-rows)

	var out []string
	for i := start; i < end; i++ {
		desc := d.sessions[i]

		// A filled bullet is live work, hollow is finished. The
		// distinction survives a monochrome terminal, which colour alone
		// would not.
		bullet := styleMuted.Render("○")
		if desc.Active() {
			bullet = statusStyle(desc.State).Render("●")
		}

		marker := "  "
		if i == s.cursor {
			marker = "> "
		}
		attached := " "
		if desc.ID == activeID {
			attached = "*"
		}

		line := fmt.Sprintf("%s%s %s %-9s %-22s %s",
			marker, attached, bullet,
			desc.Kind.Display(),
			truncateLine(desc.Title, 22),
			styleMuted.Render(desc.Summary),
		)
		if i == s.cursor {
			line = styleSelected.Render(line)
		}
		out = append(out, truncateLine(line, width))
	}
	return strings.Join(out, "\n")
}

func (s *sessionsScreen) detailLines(d *data, width int) string {
	desc, ok := s.selected(d)
	if !ok {
		return styleMuted.Render("select a session")
	}

	lines := []string{
		styleTitle.Render(desc.Title),
		styleMuted.Render(desc.ID),
		"",
		field("kind", desc.Kind.Display(), width),
		field("state", statusStyle(desc.State).Render(desc.State.Display()), width),
	}
	if desc.Repo != "" {
		lines = append(lines, field("repo", desc.Repo, width))
	}
	if desc.Summary != "" {
		lines = append(lines, field("doing", desc.Summary, width))
	}
	if !desc.StartedAt.IsZero() {
		lines = append(lines,
			field("started", desc.StartedAt.Local().Format("15:04:05"), width),
			field("elapsed", elapsed(desc), width),
		)
	}
	if desc.LastError != "" {
		lines = append(lines, "", styleErr.Render("error: "+truncateLine(desc.LastError, maxInt(10, width-7))))
	}

	lines = append(lines, "", styleMuted.Render("enter attach   n new task"))
	return strings.Join(lines, "\n")
}

// elapsed is how long the session has been going, or how long it ran.
func elapsed(desc session.Descriptor) string {
	end := time.Now()
	if !desc.Active() && !desc.UpdatedAt.IsZero() {
		end = desc.UpdatedAt
	}
	d := end.Sub(desc.StartedAt).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String()
}

func field(label, value string, width int) string {
	return fmt.Sprintf("%-8s %s", label, truncateLine(value, maxInt(10, width-9)))
}
