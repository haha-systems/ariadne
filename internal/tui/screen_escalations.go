package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/haha-systems/ariadne/internal/control"
)

// escalationsScreen shows QAC's decisions and the reasoning behind them.
//
// This view is why QAC is inspectable at all. A record that showed only
// "denied" would tell a user nothing about whether the system is rationing
// scarce cognition sensibly, so every scoring factor is displayed
// alongside the verdict.
type escalationsScreen struct {
	cursor int
	pinned bool
}

func (s *escalationsScreen) handleKey(key string, d *data) tea.Cmd {
	switch key {
	case "j", "down":
		if s.cursor < len(d.escalations)-1 {
			s.cursor++
			s.pinned = true
		}
	case "k", "up":
		if s.cursor > 0 {
			s.cursor--
			s.pinned = true
		}
	case "g", "home":
		s.cursor, s.pinned = 0, true
	case "G", "end":
		s.cursor, s.pinned = maxInt(0, len(d.escalations)-1), false
	}
	return nil
}

// selected returns the highlighted escalation.
func (s *escalationsScreen) selected(d *data) (control.EscalationSummary, bool) {
	if len(d.escalations) == 0 {
		return control.EscalationSummary{}, false
	}
	idx := s.cursor
	if !s.pinned {
		idx = len(d.escalations) - 1
	}
	if idx >= len(d.escalations) {
		idx = len(d.escalations) - 1
	}
	return d.escalations[idx], true
}

func (s *escalationsScreen) view(d *data, width, height int, focus int) string {
	if width < mediumWidth {
		top := height / 2
		return clampLines(strings.Join([]string{
			pane("ESCALATIONS", s.listLines(d, width-4, top-3), width, top, focus == 0),
			pane("DETAIL", s.detailLines(d, width-4), width, height-top, focus == 1),
		}, "\n"), height, width)
	}

	listWidth := width * 2 / 5
	left := pane("ESCALATIONS", s.listLines(d, listWidth-4, height-3), listWidth, height, focus == 0)
	right := pane("DETAIL", s.detailLines(d, width-listWidth-4), width-listWidth, height, focus == 1)
	return clampLines(joinRow(left, right), height, width)
}

func (s *escalationsScreen) listLines(d *data, width, rows int) string {
	if len(d.escalations) == 0 {
		return styleMuted.Render("no escalations yet")
	}
	if rows < 1 {
		rows = 1
	}

	selectedIdx := s.cursor
	if !s.pinned {
		selectedIdx = len(d.escalations) - 1
	}

	start := maxInt(0, selectedIdx-rows/2)
	end := minInt(len(d.escalations), start+rows)
	start = maxInt(0, end-rows)

	var out []string
	for i := start; i < end; i++ {
		esc := d.escalations[i]
		marker := "  "
		if i == selectedIdx {
			marker = "> "
		}
		// The verdict is always a word, never only a colour.
		line := fmt.Sprintf("%s %-8s %.2f  %s",
			esc.RequestedAt.Local().Format("15:04:05"),
			strings.ToUpper(esc.Decision),
			esc.Score,
			esc.Question,
		)
		out = append(out, truncateLine(marker+decisionStyle(esc.Decision).Render(line), width))
	}
	return strings.Join(out, "\n")
}

func (s *escalationsScreen) detailLines(d *data, width int) string {
	esc, ok := s.selected(d)
	if !ok {
		return styleMuted.Render("no escalation selected")
	}

	lines := []string{
		styleTitle.Render(strings.ToUpper(esc.Decision)),
		styleMuted.Render(esc.RequestedAt.Local().Format("2006-01-02 15:04:05")),
		"",
		fmt.Sprintf("from %s  to %s", orDash(esc.RequestingAgent), orDash(esc.TargetAgent)),
		"",
		styleSectionHead.Render("QUESTION"),
	}
	lines = append(lines, wrap(orDash(esc.Question), width)...)

	if esc.Reason != "" {
		lines = append(lines, "", styleSectionHead.Render("WHY IT COULD NOT BE RESOLVED LOCALLY"))
		lines = append(lines, wrap(esc.Reason, width)...)
	}

	lines = append(lines,
		"",
		styleSectionHead.Render("QAC FACTORS"),
		fmt.Sprintf("score:                  %.2f", esc.Score),
		fmt.Sprintf("uncertainty:            %.2f", esc.Uncertainty),
		fmt.Sprintf("importance:             %.2f", esc.Importance),
		fmt.Sprintf("expected gain:          %.2f", esc.ExpectedGain),
		fmt.Sprintf("failed local attempts:  %d", esc.FailedLocalAttempts),
		fmt.Sprintf("cheaper test available: %t", esc.CheaperTestAvailable),
		fmt.Sprintf("duplicate of denied:    %t", esc.Duplicate),
		fmt.Sprintf("scarcity:               %s", orDash(esc.ScarcityClass)),
	)

	lines = append(lines, "", styleSectionHead.Render("BUDGET AT DECISION"))
	if esc.Budget.CallsRemainingKnown {
		lines = append(lines, fmt.Sprintf("astra calls: %d used, %d remaining", esc.Budget.CallsUsed, esc.Budget.CallsRemaining))
	} else {
		lines = append(lines, fmt.Sprintf("astra calls: %d used (no configured limit)", esc.Budget.CallsUsed))
	}
	if esc.Budget.CooldownUntil != nil {
		lines = append(lines, "cooldown until: "+esc.Budget.CooldownUntil.Local().Format("15:04:05"))
	}

	if esc.DecisionReason != "" {
		lines = append(lines, "", styleSectionHead.Render("DECISION REASON"))
		lines = append(lines, wrap(esc.DecisionReason, width)...)
	}

	if esc.Decision == control.DecisionPending {
		lines = append(lines, "", styleWarn.Render("AWAITING DECISION — press a to approve, d to deny"))
	}

	return strings.Join(lines, "\n")
}

func decisionStyle(decision string) interface {
	Render(...string) string
} {
	switch decision {
	case control.DecisionApproved:
		return styleActive
	case control.DecisionDenied:
		return styleMuted
	case control.DecisionPending:
		return styleWarn
	default:
		return styleMuted
	}
}
