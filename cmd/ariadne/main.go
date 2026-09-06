package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	charmlog "github.com/charmbracelet/log"
	"github.com/spf13/cobra"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/issueworkflow"
	"github.com/haha-systems/ariadne/internal/proof"
)

var version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var cfgPath string

	root := &cobra.Command{
		Use:   "ariadne",
		Short: "Multi-provider coding agent orchestrator",
	}
	root.PersistentFlags().StringVar(&cfgPath, "config", "ariadne.toml", "path to ariadne.toml")

	root.AddCommand(
		runCmd(&cfgPath),
		collectProofCmd(&cfgPath),
		landCmd(&cfgPath),
		costCmd(&cfgPath),
		researchCmd(&cfgPath),
		agentsCmd(&cfgPath),
		budgetCmd(&cfgPath),
		escalationsCmd(&cfgPath),
	)
	return root
}

// runCmd starts the polling loop.
func runCmd(cfgPath *string) *cobra.Command {
	var verbose bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start polling for tasks and running agents",
		RunE: func(cmd *cobra.Command, _ []string) error {
			charmlog.SetTimeFormat("15:04:05")
			charmlog.SetReportCaller(false)
			if verbose {
				charmlog.SetLevel(charmlog.DebugLevel)
			} else {
				charmlog.SetLevel(charmlog.InfoLevel)
			}

			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			wf, err := issueworkflow.New(cfg)
			if err != nil {
				return err
			}
			return wf.Run(ctx)
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable DEBUG-level logging")
	return cmd
}

// collectProofCmd runs proof collection for a completed run.
func collectProofCmd(cfgPath *string) *cobra.Command {
	var runID string

	cmd := &cobra.Command{
		Use:   "collect-proof",
		Short: "Collect proof artifacts for a completed run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}

			worktreePath := filepath.Join(cfg.Sandbox.WorktreeDir, runID)
			summaryPath := filepath.Join(worktreePath, "proof", "summary.json")

			data, err := os.ReadFile(summaryPath)
			if err != nil {
				return fmt.Errorf("run %s: proof not found at %s: %w", runID, summaryPath, err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run-id", "", "run ID (required)")
	cmd.MarkFlagRequired("run-id") //nolint:errcheck
	return cmd
}

// landCmd safely lands a reviewed run.
func landCmd(cfgPath *string) *cobra.Command {
	var runID string

	cmd := &cobra.Command{
		Use:   "land",
		Short: "Rebase, re-run CI, and merge a reviewed run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}

			worktreePath := filepath.Join(cfg.Sandbox.WorktreeDir, runID)
			lander := proof.NewLander(proof.Config{
				RequireCIPass: true,
				PRBaseBranch:  cfg.Proof.PRBaseBranch,
			})

			sha, err := lander.Land(cmd.Context(), worktreePath)
			if err != nil {
				return fmt.Errorf("land failed: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Landed at %s\n", sha)
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run-id", "", "run ID to land (required)")
	cmd.MarkFlagRequired("run-id") //nolint:errcheck
	return cmd
}

// costCmd prints per-run cost information from proof/summary.json files.
func costCmd(cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "cost",
		Short: "Show cost summary for completed runs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}

			entries, err := os.ReadDir(cfg.Sandbox.WorktreeDir)
			if err != nil {
				return fmt.Errorf("reading worktree dir %s: %w", cfg.Sandbox.WorktreeDir, err)
			}

			var total float64
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%-30s %-12s %-10s %s\n", "RUN ID", "PROVIDER", "COST (USD)", "TASK")
			fmt.Fprintf(w, "%s\n", "------------------------------------------------------------")

			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				summaryPath := filepath.Join(cfg.Sandbox.WorktreeDir, entry.Name(), "proof", "summary.json")
				data, err := os.ReadFile(summaryPath)
				if err != nil {
					continue // run may not have a proof yet
				}
				var bundle domain.ProofBundle
				if err := json.Unmarshal(data, &bundle); err != nil {
					continue
				}
				fmt.Fprintf(w, "%-30s %-12s $%-9.4f %s\n",
					bundle.RunID, bundle.Provider, bundle.CostUSD, bundle.TaskID)
				total += bundle.CostUSD
			}

			fmt.Fprintf(w, "%s\n", "------------------------------------------------------------")
			fmt.Fprintf(w, "%-30s %-12s $%.4f\n", "TOTAL", "", total)
			return nil
		},
	}
}
