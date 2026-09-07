// Package tui is Ariadne's interactive research interface.
//
// The design rule the package exists to keep: the TUI observes and controls
// Ariadne; it does not become Ariadne. Every read and every effect goes
// through control.ResearchController, so research state, QAC decisions,
// memory, agent execution, and event history all remain fully usable with
// no Bubble Tea process anywhere in sight — through the CLI today, and
// through an API or a remote client later, without duplicating the runtime.
package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/haha-systems/ariadne/internal/control"
)

// Run starts the interface against a controller and blocks until the user
// quits. It never owns the research loop: whoever constructed the runtime
// starts and stops it, which is what makes detaching safe.
func Run(ctx context.Context, controller control.ResearchController) error {
	if controller == nil {
		return fmt.Errorf("tui: nil controller")
	}

	model := New(ctx, controller)
	program := tea.NewProgram(model, tea.WithContext(ctx))

	if _, err := program.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}
