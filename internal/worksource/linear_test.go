package worksource

import (
	"testing"
	"time"

	"github.com/haha-systems/ariadne/internal/domain"
)

func TestNewLinearSource_MissingToken(t *testing.T) {
	_, err := NewLinearSource("", "team-123", nil)
	if err == nil {
		t.Error("expected error for missing token")
	}
}

func TestNewLinearSource_MissingTeamID(t *testing.T) {
	_, err := NewLinearSource("token", "", nil)
	if err == nil {
		t.Error("expected error for missing team_id")
	}
}

func TestNewLinearSource_Valid(t *testing.T) {
	s, err := NewLinearSource("token", "team-123", []string{"Todo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Name() != "linear" {
		t.Errorf("unexpected name: %s", s.Name())
	}
	if s.claimedState != "Todo" {
		t.Errorf("expected claimedState=Todo, got %s", s.claimedState)
	}
}

func TestLinearIssueToTask_FrontMatter(t *testing.T) {
	issue := linearIssue{
		ID:          "issue-abc",
		Title:       "Add retry logic",
		Description: "---\nariadne:\n  agent: claude\n---\nImplement retries",
		URL:         "https://linear.app/team/issue/ENG-42",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	issue.Labels.Nodes = []struct{ Name string `json:"name"` }{
		{Name: "ariadne"},
		{Name: "backend"},
	}

	task := linearIssueToTask(issue)

	if task.ID != "issue-abc" {
		t.Errorf("unexpected id: %s", task.ID)
	}
	if task.Source != "linear" {
		t.Errorf("unexpected source: %s", task.Source)
	}
	if task.Config == nil || task.Config.Agent != "claude" {
		t.Errorf("expected front-matter parsed, got %v", task.Config)
	}
	if task.Description != "Implement retries" {
		t.Errorf("unexpected description: %q", task.Description)
	}
	if task.Status != domain.TaskStatusPending {
		t.Errorf("expected pending status")
	}
	if len(task.Labels) != 2 {
		t.Errorf("expected 2 labels, got %d", len(task.Labels))
	}
}

func TestNewLinearSource_DefaultClaimedState(t *testing.T) {
	s, err := NewLinearSource("token", "team-123", nil)
	if err != nil {
		t.Fatal(err)
	}
	// No state filter — claimedState should fall back to default.
	if s.claimedState != "In Progress" {
		t.Errorf("expected default claimedState='In Progress', got %q", s.claimedState)
	}
}
