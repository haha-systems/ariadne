package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/runstate"
)

func runsCmd(cfgPath *string) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "runs",
		Short: "List known Ariadne runs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = godotenv.Load()
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			records, err := runStateStore(cfg).List()
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(records)
			}
			renderRunsTable(cmd.OutOrStdout(), records)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print runs as JSON")
	return cmd
}

func inspectRunCmd(cfgPath *string) *cobra.Command {
	var runID string
	var asJSON bool
	var lines int

	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Inspect a single run and its recent logs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = godotenv.Load()
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			record, err := runStateStore(cfg).Get(runID)
			if err != nil {
				return err
			}
			logs := []runstate.LogEntry{}
			if record.LogPath != "" {
				logs, _ = runstate.TailLog(record.LogPath, lines)
			}
			payload := struct {
				Run  *runstate.Record    `json:"run"`
				Logs []runstate.LogEntry `json:"logs"`
			}{
				Run:  record,
				Logs: logs,
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(payload)
			}
			renderRunDetail(cmd.OutOrStdout(), record, logs)
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run-id", "", "Run ID to inspect")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the inspection payload as JSON")
	cmd.Flags().IntVar(&lines, "lines", 20, "Number of recent log lines to include")
	cmd.MarkFlagRequired("run-id") //nolint:errcheck
	return cmd
}

func monitorCmd(cfgPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Monitor Ariadne runs in a live terminal UI",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = godotenv.Load()
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			model := newMonitorModel(runStateStore(cfg))
			program := tea.NewProgram(model, tea.WithAltScreen())
			_, err = program.Run()
			return err
		},
	}
	return cmd
}

func renderRunsTable(w io.Writer, records []runstate.Record) {
	fmt.Fprintf(w, "%-24s %-10s %-10s %-12s %s\n", "RUN ID", "STATUS", "PROVIDER", "UPDATED", "TASK")
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 88))
	for _, record := range records {
		fmt.Fprintf(
			w,
			"%-24s %-10s %-10s %-12s %s\n",
			record.ID,
			record.Status,
			record.Provider,
			humanAge(record.UpdatedAt),
			record.TaskTitle,
		)
	}
}

func renderRunDetail(w io.Writer, record *runstate.Record, logs []runstate.LogEntry) {
	fmt.Fprintf(w, "Run:      %s\n", record.ID)
	fmt.Fprintf(w, "Task:     %s (%s)\n", record.TaskTitle, record.TaskID)
	fmt.Fprintf(w, "Type:     %s\n", record.TaskType)
	fmt.Fprintf(w, "Source:   %s\n", record.TaskSource)
	fmt.Fprintf(w, "Provider: %s\n", record.Provider)
	if record.Persona != "" {
		fmt.Fprintf(w, "Persona:  %s\n", record.Persona)
	}
	fmt.Fprintf(w, "Status:   %s\n", record.Status)
	fmt.Fprintf(w, "Updated:  %s\n", record.UpdatedAt.Format(time.RFC3339))
	if record.WorktreePath != "" {
		fmt.Fprintf(w, "Worktree: %s\n", record.WorktreePath)
	}
	if record.PRURL != "" {
		fmt.Fprintf(w, "PR:       %s\n", record.PRURL)
	}
	if record.LastError != "" {
		fmt.Fprintf(w, "Error:    %s\n", record.LastError)
	}
	if len(logs) == 0 {
		return
	}
	fmt.Fprintln(w, "\nRecent logs:")
	for _, entry := range logs {
		text := entry.Line
		if text == "" {
			text = entry.Event
		}
		fmt.Fprintf(w, "[%s] %s\n", entry.Event, text)
	}
}

type monitorTickMsg struct{}

type monitorModel struct {
	store    *runstate.Store
	records  []runstate.Record
	logs     []runstate.LogEntry
	selected int
	err      error
	width    int
	height   int
}

func newMonitorModel(store *runstate.Store) monitorModel {
	return monitorModel{store: store}
}

func (m monitorModel) Init() tea.Cmd {
	return tea.Batch(m.reload(), tickMonitor())
}

func (m monitorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case monitorTickMsg:
		return m, tea.Batch(m.reload(), tickMonitor())
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
			return m, m.loadLogs()
		case "down", "j":
			if m.selected < len(m.records)-1 {
				m.selected++
			}
			return m, m.loadLogs()
		case "r":
			return m, m.reload()
		}
	case []runstate.Record:
		m.records = msg
		if m.selected >= len(m.records) && len(m.records) > 0 {
			m.selected = len(m.records) - 1
		}
		if len(m.records) == 0 {
			m.logs = nil
			return m, nil
		}
		return m, m.loadLogs()
	case monitorLogsMsg:
		m.logs = msg.entries
		return m, nil
	case monitorErrMsg:
		m.err = msg.err
		return m, nil
	}
	return m, nil
}

func (m monitorModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Render("Ariadne Monitor")
	help := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("up/down: select  r: refresh  q: quit")
	header := title + "\n" + help + "\n\n"

	if m.err != nil {
		header += lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(m.err.Error()) + "\n\n"
	}
	if len(m.records) == 0 {
		return header + "No runs recorded yet."
	}

	leftWidth := m.width / 2
	if leftWidth < 48 {
		leftWidth = 48
	}
	rightWidth := m.width - leftWidth - 3
	if rightWidth < 32 {
		rightWidth = 32
	}

	var rows []string
	for i, record := range m.records {
		cursor := " "
		if i == m.selected {
			cursor = ">"
		}
		row := fmt.Sprintf("%s %-10s %-9s %-10s %s",
			cursor,
			record.Status,
			record.Provider,
			humanAge(record.UpdatedAt),
			truncate(record.TaskTitle, leftWidth-36),
		)
		if i == m.selected {
			row = lipgloss.NewStyle().Bold(true).Render(row)
		}
		rows = append(rows, row)
	}

	selected := m.records[m.selected]
	right := []string{
		fmt.Sprintf("Run: %s", selected.ID),
		fmt.Sprintf("Task: %s", selected.TaskTitle),
		fmt.Sprintf("Provider: %s", selected.Provider),
		fmt.Sprintf("Status: %s", selected.Status),
		fmt.Sprintf("Last event: %s", selected.LastEvent),
	}
	if selected.Persona != "" {
		right = append(right, fmt.Sprintf("Persona: %s", selected.Persona))
	}
	if selected.LastError != "" {
		right = append(right, "Error: "+selected.LastError)
	}
	right = append(right, "", "Recent logs:")
	for _, entry := range m.logs {
		text := entry.Line
		if text == "" {
			text = entry.Event
		}
		right = append(right, truncate(fmt.Sprintf("[%s] %s", entry.Event, text), rightWidth))
	}

	leftPane := lipgloss.NewStyle().Width(leftWidth).Render(strings.Join(rows, "\n"))
	rightPane := lipgloss.NewStyle().Width(rightWidth).Render(strings.Join(right, "\n"))
	return header + lipgloss.JoinHorizontal(lipgloss.Top, leftPane, "   ", rightPane)
}

type monitorLogsMsg struct {
	entries []runstate.LogEntry
}

type monitorErrMsg struct {
	err error
}

func (m monitorModel) reload() tea.Cmd {
	return func() tea.Msg {
		records, err := m.store.List()
		if err != nil {
			return monitorErrMsg{err: err}
		}
		sort.Slice(records, func(i, j int) bool {
			return records[i].UpdatedAt.After(records[j].UpdatedAt)
		})
		return records
	}
}

func (m monitorModel) loadLogs() tea.Cmd {
	if len(m.records) == 0 {
		return nil
	}
	record := m.records[m.selected]
	return func() tea.Msg {
		if record.LogPath == "" {
			return monitorLogsMsg{}
		}
		entries, err := runstate.TailLog(record.LogPath, 20)
		if err != nil {
			return monitorErrMsg{err: err}
		}
		return monitorLogsMsg{entries: entries}
	}
}

func tickMonitor() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return monitorTickMsg{}
	})
}

func truncate(s string, width int) string {
	if width <= 0 || len(s) <= width {
		return s
	}
	if width <= 1 {
		return s[:width]
	}
	if width <= 3 {
		return s[:width]
	}
	return s[:width-3] + "..."
}

func humanAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
