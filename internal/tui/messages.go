package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/haha-systems/ariadne/internal/control"
)

// Messages are the only way runtime data enters the Bubble Tea model.
// Update never calls the controller directly: every read is a tea.Cmd that
// runs off the event loop and returns one of these, so a slow snapshot or a
// blocked memory backend can never freeze the interface.

// SnapshotMsg carries a fresh research projection.
type SnapshotMsg struct {
	Snapshot control.ResearchSnapshot
	Err      error
}

// RuntimeEventMsg carries one structured runtime event.
type RuntimeEventMsg struct{ Event control.Event }

// AgentLogMsg carries one line of agent output.
type AgentLogMsg struct{ Entry control.AgentLogEntry }

// EventsClosedMsg reports that the live event stream ended, so the model
// can stop showing itself as live.
type EventsClosedMsg struct{}

// LogsClosedMsg reports that the live log stream ended.
type LogsClosedMsg struct{}

// HistoryMsg carries the replayed context loaded at startup.
type HistoryMsg struct {
	Events []control.Event
	Logs   []control.AgentLogEntry
	Err    error
}

// EscalationsMsg carries the QAC escalation records.
type EscalationsMsg struct {
	Escalations []control.EscalationSummary
	Err         error
}

// MemoryMsg carries the structured memory view.
type MemoryMsg struct {
	View control.MemoryView
	Err  error
}

// SteeringMsg carries the project's steering records.
type SteeringMsg struct {
	Records []control.SteeringRecord
	Err     error
}

// CommandResultMsg reports the outcome of a submitted command.
type CommandResultMsg struct {
	Command string
	Err     error
}

// TickMsg drives periodic refresh.
type TickMsg time.Time

// tickInterval is how often the model re-polls the snapshot. Events drive
// most updates; this exists so elapsed timers advance and so a missed event
// cannot leave the display permanently stale.
const tickInterval = time.Second

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return TickMsg(t) })
}

// fetchSnapshot reads the current projection off the event loop.
func fetchSnapshot(ctx context.Context, c control.ResearchController) tea.Cmd {
	return func() tea.Msg {
		snap, err := c.Snapshot(ctx)
		return SnapshotMsg{Snapshot: snap, Err: err}
	}
}

func fetchEscalations(ctx context.Context, c control.ResearchController) tea.Cmd {
	return func() tea.Msg {
		esc, err := c.Escalations(ctx, 200)
		return EscalationsMsg{Escalations: esc, Err: err}
	}
}

func fetchMemory(ctx context.Context, c control.ResearchController) tea.Cmd {
	return func() tea.Msg {
		view, err := c.Memory(ctx)
		return MemoryMsg{View: view, Err: err}
	}
}

func fetchSteering(ctx context.Context, c control.ResearchController) tea.Cmd {
	return func() tea.Msg {
		records, err := c.Steering(ctx)
		return SteeringMsg{Records: records, Err: err}
	}
}

// fetchHistory replays recent persisted events and buffered logs so the
// interface opens with context instead of a blank screen.
func fetchHistory(ctx context.Context, c control.ResearchController, limit int) tea.Cmd {
	return func() tea.Msg {
		events, err := c.RecentEvents(ctx, limit)
		logs, logErr := c.AgentLog(ctx, "", limit)
		if err == nil {
			err = logErr
		}
		return HistoryMsg{Events: events, Logs: logs, Err: err}
	}
}

// sendCommand submits a command off the event loop. Update never blocks on
// the runtime, so a command that has to touch memory or wake a scheduler
// cannot stall a keystroke.
func sendCommand(ctx context.Context, c control.ResearchController, cmd control.ResearchCommand) tea.Cmd {
	return func() tea.Msg {
		err := c.Send(ctx, cmd)
		return CommandResultMsg{Command: control.CommandName(cmd), Err: err}
	}
}

// waitForEvent turns one receive on the event channel into a message. Each
// message re-arms the next receive, which is the idiomatic way to bridge a
// Go channel into Bubble Tea without a goroutine writing to the model.
func waitForEvent(ch <-chan control.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return EventsClosedMsg{}
		}
		return RuntimeEventMsg{Event: ev}
	}
}

func waitForLog(ch <-chan control.AgentLogEntry) tea.Cmd {
	return func() tea.Msg {
		entry, ok := <-ch
		if !ok {
			return LogsClosedMsg{}
		}
		return AgentLogMsg{Entry: entry}
	}
}
