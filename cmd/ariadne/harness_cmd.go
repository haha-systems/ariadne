package main

import (
	"fmt"
	"os/signal"
	"syscall"

	charmlog "github.com/charmbracelet/log"
	"github.com/spf13/cobra"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/harness"
	"github.com/haha-systems/ariadne/internal/tui"
)

// harnessCmd is what `ariadne` with no arguments does. Running the binary
// bare should show you your work, not a wall of help text: the subcommands
// remain for scripting and for CI, where an interface would be in the way.
func harnessCmd(cfgPath *string) *cobra.Command {
	var noPoll bool

	cmd := &cobra.Command{
		Use:   "harness",
		Short: "Open the unified interface over every running session",
		Long: "Open Ariadne's interface. Issue runs and research programmes appear\n" +
			"in one session list; press 0 to switch between them.\n\n" +
			"Quitting the interface does not stop the work. Sessions keep running\n" +
			"in this process until it exits, and pausing a session is an explicit\n" +
			"choice offered on quit.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The interface owns the screen, so log lines must not be
			// written to it. Warnings still reach the alert area through
			// the session list.
			charmlog.SetLevel(charmlog.FatalLevel)

			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			h, err := harness.New(cfg)
			if err != nil {
				return err
			}

			if !noPoll {
				if err := h.StartIssueWorkflow(ctx); err != nil {
					return fmt.Errorf("starting issue workflow: %w", err)
				}
			}

			// The local lane is what makes the harness a place you start
			// work, not only a place you watch it. Failing to start it is
			// reported rather than fatal: watching existing sessions is
			// still worth doing.
			if err := h.StartLocalLane(ctx); err != nil {
				charmlog.Warn("ad-hoc tasks unavailable", "reason", err)
			}

			if err := tui.RunHarness(ctx, h.Host()); err != nil {
				return err
			}

			// Leaving the interface cancels the workflows this process
			// started, then waits for them: exiting while a supervised
			// run is mid-flight would orphan a worktree.
			stop()
			return h.Close()
		},
	}
	cmd.Flags().BoolVar(&noPoll, "no-poll", false,
		"Open the interface without polling the work source for new issues")
	return cmd
}
