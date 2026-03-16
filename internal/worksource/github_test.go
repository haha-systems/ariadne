package worksource

import (
	"testing"

	gh "github.com/google/go-github/v68/github"

	"github.com/haha-systems/conductor/internal/domain"
)

func TestNewGitHubSource_InvalidRepo(t *testing.T) {
	cases := []string{"", "noslash", "/nope", "nope/"}
	for _, repo := range cases {
		_, err := NewGitHubSource("token", repo, nil)
		if err == nil {
			t.Errorf("expected error for repo %q", repo)
		}
	}
}

func TestNewGitHubSource_ValidRepo(t *testing.T) {
	s, err := NewGitHubSource("token", "org/repo", []string{"conductor"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Name() != "github" {
		t.Errorf("unexpected name: %s", s.Name())
	}
}

func TestHasLabel(t *testing.T) {
	issue := &gh.Issue{
		Labels: []*gh.Label{
			{Name: gh.Ptr("conductor")},
			{Name: gh.Ptr("bug")},
		},
	}
	if !hasLabel(issue, "conductor") {
		t.Error("expected hasLabel to return true for 'conductor'")
	}
	if hasLabel(issue, "enhancement") {
		t.Error("expected hasLabel to return false for 'enhancement'")
	}
}

func TestMatchesFilter(t *testing.T) {
	s := &GitHubSource{labelFilter: []string{"conductor", "ready"}}

	issue := &gh.Issue{
		Labels: []*gh.Label{
			{Name: gh.Ptr("conductor")},
			{Name: gh.Ptr("ready")},
		},
	}
	if !s.matchesFilter(issue) {
		t.Error("expected matchesFilter to return true")
	}

	issueMissing := &gh.Issue{
		Labels: []*gh.Label{
			{Name: gh.Ptr("conductor")},
		},
	}
	if s.matchesFilter(issueMissing) {
		t.Error("expected matchesFilter to return false when label missing")
	}
}

func TestIssueToTask_FrontMatter(t *testing.T) {
	body := "---\nconductor:\n  agent: claude\n---\nDo the thing"
	num := 42
	issue := &gh.Issue{
		Number: &num,
		Title:  gh.Ptr("My Issue"),
		Body:   gh.Ptr(body),
		Labels: []*gh.Label{
			{Name: gh.Ptr("conductor")},
		},
		HTMLURL: gh.Ptr("https://github.com/org/repo/issues/42"),
	}

	task := issueToTask(issue, "org/repo")

	if task.ID != "42" {
		t.Errorf("expected ID=42, got %s", task.ID)
	}
	if task.Config == nil {
		t.Fatal("expected parsed front-matter config")
	}
	if task.Config.Agent != "claude" {
		t.Errorf("expected agent=claude, got %q", task.Config.Agent)
	}
	if task.Description != "Do the thing" {
		t.Errorf("unexpected description: %q", task.Description)
	}
	if task.Status != domain.TaskStatusPending {
		t.Errorf("expected pending status")
	}
}
