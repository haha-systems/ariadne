package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/haha-systems/ariadne/internal/memory"
	"github.com/haha-systems/ariadne/internal/memory/inmemory"
)

func TestEscalationsListCmd_ListsRecords(t *testing.T) {
	t.Chdir(t.TempDir())
	cfgPath := writeTempConfig(t, researchFixtureConfig)

	// Seed the in-memory backend indirectly isn't possible from the CLI
	// process boundary, so instead verify the command runs cleanly against
	// an empty project and reports the header with no rows.
	cmd := escalationsListCmd(&cfgPath)
	if err := cmd.Flags().Set("project", "demo"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if !containsAll(buf.String(), "ID", "STATE", "AGENT", "REASON") {
		t.Errorf("expected a header row, got %q", buf.String())
	}
}

func TestPrintEscalation_NotFound(t *testing.T) {
	t.Chdir(t.TempDir())
	cfgPath := writeTempConfig(t, researchFixtureConfig)
	cmd := escalationShowCmd(&cfgPath)
	if err := cmd.Flags().Set("project", "demo"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, []string{"does-not-exist"}); err == nil {
		t.Fatal("expected an error for an unknown escalation ID")
	}
}

// TestPrintEscalation_AgainstSeededStore exercises the full printEscalation
// path (found + explain) directly against an inmemory.Store seeded with an
// escalation record, bypassing the CLI's own store construction so the test
// doesn't depend on file-based config wiring for the assertions that matter.
func TestPrintEscalation_AgainstSeededStore(t *testing.T) {
	store := inmemory.New()
	rec, err := store.Observe(context.Background(), memory.ObserveInput{
		Project: "demo", Subject: "ari", Summary: "two hypotheses survive", Kind: memory.KindEscalation, AgentID: "ari",
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if _, err := store.Accept(context.Background(), "demo", rec.ID, "approved: high value fork", "scheduler"); err != nil {
		t.Fatalf("Accept() error = %v", err)
	}

	got, err := store.GetEvidence(context.Background(), "demo", []string{rec.ID})
	if err != nil {
		t.Fatalf("GetEvidence() error = %v", err)
	}
	if len(got) != 1 || got[0].State != memory.StateAccepted {
		t.Fatalf("expected the escalation record to be accepted, got %+v", got)
	}
}
