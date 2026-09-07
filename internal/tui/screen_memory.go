package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/haha-systems/ariadne/internal/control"
)

// memoryScreen browses research memory.
//
// It is read-only by design. Observations and evidence are append-only
// facts the research programme reasons from; letting an interface edit them
// would turn the audit trail into something a user could quietly rewrite.
// Steering exists precisely so a user can influence research without doing
// that.
type memoryScreen struct {
	cursor int
}

// memorySection is one titled group of records.
type memorySection struct {
	title   string
	records []control.MemoryRecord
}

func (s *memoryScreen) sections(d *data) []memorySection {
	view := d.memory
	sections := []memorySection{
		{"FRONTIER", nonEmpty(view.Frontier)},
		{"LATEST OBSERVATIONS", view.Observations},
		{"ACTIVE HYPOTHESES", view.ActiveHypotheses},
		{"REJECTED HYPOTHESES", view.RejectedHypotheses},
		{"OPEN QUESTIONS", view.OpenQuestions},
		{"KNOWN CONFOUNDS", view.Confounds},
		{"RECENT DECISIONS", view.Decisions},
		{"LATEST ASTRA RESULT", nonEmpty(view.LatestAstraResult)},
	}

	out := make([]memorySection, 0, len(sections))
	for _, section := range sections {
		if len(section.records) > 0 {
			out = append(out, section)
		}
	}
	return out
}

// flatten produces the selectable list: section headers are not selectable,
// only records are.
func (s *memoryScreen) flatten(d *data) []control.MemoryRecord {
	var out []control.MemoryRecord
	for _, section := range s.sections(d) {
		out = append(out, section.records...)
	}
	return out
}

func (s *memoryScreen) handleKey(key string, d *data) tea.Cmd {
	records := s.flatten(d)
	switch key {
	case "j", "down":
		if s.cursor < len(records)-1 {
			s.cursor++
		}
	case "k", "up":
		if s.cursor > 0 {
			s.cursor--
		}
	case "g", "home":
		s.cursor = 0
	case "G", "end":
		s.cursor = maxInt(0, len(records)-1)
	}
	return nil
}

func (s *memoryScreen) view(d *data, width, height int, focus int) string {
	title := "MEMORY"
	if d.memory.Backend != "" {
		// The backend is metadata: nothing in this view behaves
		// differently because it happens to be Ghostdive.
		title += " (" + d.memory.Backend + ")"
	}

	if width < mediumWidth {
		top := height / 2
		return clampLines(strings.Join([]string{
			pane(title, s.listLines(d, width-4, top-3), width, top, focus == 0),
			pane("RECORD", s.detailLines(d, width-4), width, height-top, focus == 1),
		}, "\n"), height, width)
	}

	listWidth := width * 3 / 5
	left := pane(title, s.listLines(d, listWidth-4, height-3), listWidth, height, focus == 0)
	right := pane("RECORD", s.detailLines(d, width-listWidth-4), width-listWidth, height, focus == 1)
	return clampLines(joinRow(left, right), height, width)
}

func (s *memoryScreen) listLines(d *data, width, rows int) string {
	sections := s.sections(d)
	if len(sections) == 0 {
		return styleMuted.Render("no research memory recorded yet")
	}
	if rows < 1 {
		rows = 1
	}

	var lines []string
	idx := 0
	for _, section := range sections {
		lines = append(lines, styleSectionHead.Render(section.title))
		for _, rec := range section.records {
			marker := "  "
			text := truncateLine(rec.Summary, width-4)
			if idx == s.cursor {
				marker = "> "
				text = styleSelected.Render(text)
			}
			lines = append(lines, marker+text)
			idx++
		}
		lines = append(lines, "")
	}

	// Scroll so the cursor stays visible.
	cursorLine := s.cursorLine(d)
	start := maxInt(0, cursorLine-rows/2)
	end := minInt(len(lines), start+rows)
	start = maxInt(0, end-rows)
	return strings.Join(lines[start:end], "\n")
}

// cursorLine maps the record cursor onto a rendered line index.
func (s *memoryScreen) cursorLine(d *data) int {
	line, idx := 0, 0
	for _, section := range s.sections(d) {
		line++ // header
		for range section.records {
			if idx == s.cursor {
				return line
			}
			line++
			idx++
		}
		line++ // blank separator
	}
	return line
}

func (s *memoryScreen) detailLines(d *data, width int) string {
	records := s.flatten(d)
	if len(records) == 0 {
		return styleMuted.Render("no record selected")
	}
	if s.cursor >= len(records) {
		s.cursor = len(records) - 1
	}
	rec := records[s.cursor]

	lines := []string{
		styleTitle.Render(strings.ToUpper(orDash(rec.Kind))),
		styleMuted.Render(rec.CreatedAt.Local().Format("2006-01-02 15:04:05")),
		"",
		"state:   " + orDash(rec.State),
		"agent:   " + orDash(rec.AgentID),
		"subject: " + orDash(rec.Subject),
	}
	if rec.Confidence > 0 {
		lines = append(lines, fmt.Sprintf("confidence: %.2f", rec.Confidence))
	}

	lines = append(lines, "", styleSectionHead.Render("SUMMARY"))
	lines = append(lines, wrap(rec.Summary, width)...)

	if len(rec.EvidenceRefs) > 0 {
		lines = append(lines, "", styleSectionHead.Render("EVIDENCE"))
		for _, ref := range rec.EvidenceRefs {
			lines = append(lines, "- "+ref)
		}
	}
	if rec.Reason != "" {
		lines = append(lines, "", styleSectionHead.Render("REASON"))
		lines = append(lines, wrap(rec.Reason, width)...)
	}

	lines = append(lines, "", styleMuted.Render("read-only — use s to steer research instead"))
	return strings.Join(lines, "\n")
}

func nonEmpty(rec control.MemoryRecord) []control.MemoryRecord {
	if strings.TrimSpace(rec.Summary) == "" {
		return nil
	}
	return []control.MemoryRecord{rec}
}
