package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/memory"
)

// escalationsCmd and its subcommands are project-scoped: memory.Store scopes
// every read to a project (see internal/memory.Store's doc comment), so
// unlike the spec's illustrative `ariadne escalations` / `explain-escalation
// <id>`, these commands take --project explicitly.
func escalationsCmd(cfgPath *string) *cobra.Command {
	root := &cobra.Command{
		Use:   "escalations",
		Short: "List recorded escalation requests for a project",
	}
	root.AddCommand(escalationsListCmd(cfgPath), escalationShowCmd(cfgPath), explainEscalationCmd(cfgPath))
	return root
}

func escalationsListCmd(cfgPath *string) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List escalation records for a project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			store, err := buildMemoryStore(cfg)
			if err != nil {
				return err
			}
			records, err := store.Retrieve(cmd.Context(), memory.RetrieveInput{Project: project, Kinds: []memory.Kind{memory.KindEscalation}})
			if err != nil {
				return fmt.Errorf("list escalations: %w", err)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%-24s %-10s %-30s %s\n", "ID", "STATE", "AGENT", "REASON")
			for _, rec := range records {
				fmt.Fprintf(w, "%-24s %-10s %-30s %s\n", rec.ID, rec.State, rec.AgentID, rec.Summary)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "research project (required)")
	cmd.MarkFlagRequired("project") //nolint:errcheck
	return cmd
}

func escalationShowCmd(cfgPath *string) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a single escalation record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return printEscalation(cmd, cfgPath, project, args[0], false)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "research project (required)")
	cmd.MarkFlagRequired("project") //nolint:errcheck
	return cmd
}

func explainEscalationCmd(cfgPath *string) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "explain <id>",
		Short: "Explain why an escalation was approved or denied",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return printEscalation(cmd, cfgPath, project, args[0], true)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "research project (required)")
	cmd.MarkFlagRequired("project") //nolint:errcheck
	return cmd
}

func printEscalation(cmd *cobra.Command, cfgPath *string, project, id string, explain bool) error {
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	store, err := buildMemoryStore(cfg)
	if err != nil {
		return err
	}
	records, err := store.GetEvidence(cmd.Context(), project, []string{id})
	if err != nil {
		return fmt.Errorf("escalation %q not found in project %q: %w", id, project, err)
	}
	rec := records[0]

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "ID:      %s\n", rec.ID)
	fmt.Fprintf(w, "State:   %s\n", rec.State)
	fmt.Fprintf(w, "Agent:   %s\n", rec.AgentID)
	fmt.Fprintf(w, "Reason:  %s\n", rec.Summary)
	if explain {
		fmt.Fprintln(w, "\nDecision:")
		switch rec.State {
		case memory.StateAccepted:
			fmt.Fprintln(w, "  ESCALATE — approved. See the decisions list for what Astra returned.")
		case memory.StateRejected:
			fmt.Fprintln(w, "  DENY — the escalation was not approved (see Reason above for why).")
		default:
			fmt.Fprintf(w, "  Not yet decided (state: %s).\n", rec.State)
		}
	}
	return nil
}
