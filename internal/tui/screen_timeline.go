package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// timelineScreen is the chronological view of structured runtime events.
//
// It is kept strictly separate from the agent log views: raw model output
// answers "what is the agent doing", structured events answer "what
// happened in the runtime". Merging the two data models would make both
// harder to read and neither easier to reason about.
type timelineScreen struct {
	cursor int
	// pinned is false while the cursor sits at the newest event, so new
	// events keep the view current until the user deliberately scrolls back.
	pinned bool
}

// notifyAppended keeps an unpinned timeline tracking the newest event.
func (s *timelineScreen) notifyAppended(total int) {
	if !s.pinned {
		s.cursor = maxInt(0, total-1)
	}
}

func (s *timelineScreen) handleKey(key string, d *data) tea.Cmd {
	switch key {
	case "j", "down":
		if s.cursor < len(d.events)-1 {
			s.cursor++
		}
		s.pinned = s.cursor < len(d.events)-1
	case "k", "up":
		if s.cursor > 0 {
			s.cursor--
			s.pinned = true
		}
	case "g", "home":
		s.cursor, s.pinned = 0, true
	case "G", "end":
		s.cursor, s.pinned = maxInt(0, len(d.events)-1), false
	}
	return nil
}

func (s *timelineScreen) view(d *data, width, height int, focus int) string {
	if width < mediumWidth {
		top := height * 2 / 3
		return clampLines(strings.Join([]string{
			pane("TIMELINE", s.listLines(d, width-4, top-3), width, top, focus == 0),
			pane("EVENT DETAIL", s.detailLines(d, width-4), width, height-top, focus == 1),
		}, "\n"), height, width)
	}

	listWidth := width * 3 / 5
	left := pane("TIMELINE", s.listLines(d, listWidth-4, height-3), listWidth, height, focus == 0)
	right := pane("EVENT DETAIL", s.detailLines(d, width-listWidth-4), width-listWidth, height, focus == 1)
	return clampLines(joinRow(left, right), height, width)
}

func (s *timelineScreen) listLines(d *data, width, rows int) string {
	if len(d.events) == 0 {
		return styleMuted.Render("no events yet")
	}
	if rows < 1 {
		rows = 1
	}

	if s.cursor >= len(d.events) {
		s.cursor = len(d.events) - 1
	}

	// Keep the cursor inside the window.
	start := s.cursor - rows/2
	if start < 0 {
		start = 0
	}
	end := minInt(len(d.events), start+rows)
	start = maxInt(0, end-rows)

	var out []string
	for i := start; i < end; i++ {
		marker := "  "
		line := formatEventLine(d.events[i])
		if i == s.cursor {
			marker = "> "
			line = styleSelected.Render(line)
		}
		out = append(out, truncateLine(marker+line, width))
	}
	return strings.Join(out, "\n")
}

func (s *timelineScreen) detailLines(d *data, width int) string {
	if len(d.events) == 0 || s.cursor >= len(d.events) {
		return styleMuted.Render("select an event")
	}
	ev := d.events[s.cursor]

	lines := []string{
		styleTitle.Render(ev.Name),
		styleMuted.Render(ev.Time.Local().Format("2006-01-02 15:04:05.000")),
		"",
	}

	// Fields are sorted so the same event always renders identically —
	// a detail pane that reshuffles on every redraw is unreadable.
	keys := make([]string, 0, len(ev.Fields))
	for k := range ev.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		value := fmt.Sprintf("%v", ev.Fields[k])
		wrapped := wrap(value, maxInt(10, width-len(k)-2))
		if len(wrapped) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", k, wrapped[0]))
		for _, cont := range wrapped[1:] {
			lines = append(lines, strings.Repeat(" ", len(k)+2)+cont)
		}
	}
	return strings.Join(lines, "\n")
}
