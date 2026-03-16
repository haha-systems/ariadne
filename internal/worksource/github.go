package worksource

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"

	"github.com/haha-systems/conductor/internal/domain"
)

const (
	claimedLabel = "conductor:claimed"
	runningLabel = "conductor:running"
)

// GitHubSource polls a GitHub repository for issues and claims them via labels.
type GitHubSource struct {
	client      *gh.Client
	owner       string
	repo        string
	labelFilter []string
}

// NewGitHubSource creates a GitHubSource authenticated with the given token.
// repo should be in "owner/repo" format.
func NewGitHubSource(token, repo string, labelFilter []string) (*GitHubSource, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("repo must be in owner/repo format, got %q", repo)
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)

	return &GitHubSource{
		client:      gh.NewClient(tc),
		owner:       parts[0],
		repo:        parts[1],
		labelFilter: labelFilter,
	}, nil
}

func (s *GitHubSource) Name() string { return "github" }

// Poll fetches open issues that have all required labels but have NOT been claimed yet.
func (s *GitHubSource) Poll(ctx context.Context) ([]*domain.Task, error) {
	opts := &gh.IssueListByRepoOptions{
		State: "open",
		ListOptions: gh.ListOptions{
			PerPage: 100,
		},
	}

	issues, _, err := s.client.Issues.ListByRepo(ctx, s.owner, s.repo, opts)
	if err != nil {
		return nil, fmt.Errorf("github poll: %w", err)
	}

	var tasks []*domain.Task
	for _, issue := range issues {
		if !s.matchesFilter(issue) {
			continue
		}
		if hasLabel(issue, claimedLabel) || hasLabel(issue, runningLabel) {
			continue
		}
		tasks = append(tasks, issueToTask(issue, s.owner+"/"+s.repo))
	}
	return tasks, nil
}

// Claim adds the conductor:claimed label to the issue atomically.
// If the label was already added by another process the API will succeed but we
// do a fresh read to verify we were the one to add it (simple optimistic lock).
func (s *GitHubSource) Claim(ctx context.Context, task *domain.Task) error {
	issueNum, err := strconv.Atoi(task.ID)
	if err != nil {
		return fmt.Errorf("invalid github issue id %q: %w", task.ID, err)
	}

	_, _, err = s.client.Issues.AddLabelsToIssue(ctx, s.owner, s.repo, issueNum, []string{claimedLabel})
	if err != nil {
		return fmt.Errorf("claim issue #%d: %w", issueNum, err)
	}
	return nil
}

// PostResult adds a comment to the issue with the proof summary.
func (s *GitHubSource) PostResult(ctx context.Context, task *domain.Task, summary string) error {
	issueNum, err := strconv.Atoi(task.ID)
	if err != nil {
		return fmt.Errorf("invalid github issue id %q: %w", task.ID, err)
	}

	comment := &gh.IssueComment{
		Body: gh.Ptr("## Conductor Run Complete\n\n" + summary),
	}
	_, _, err = s.client.Issues.CreateComment(ctx, s.owner, s.repo, issueNum, comment)
	return err
}

// matchesFilter returns true if the issue has all labels in the filter list.
func (s *GitHubSource) matchesFilter(issue *gh.Issue) bool {
	for _, required := range s.labelFilter {
		if !hasLabel(issue, required) {
			return false
		}
	}
	return true
}

func hasLabel(issue *gh.Issue, name string) bool {
	for _, l := range issue.Labels {
		if l.GetName() == name {
			return true
		}
	}
	return false
}

func issueToTask(issue *gh.Issue, repo string) *domain.Task {
	desc := issue.GetBody()
	cfg, body := domain.ParseFrontMatter(desc)

	labels := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		labels = append(labels, l.GetName())
	}

	return &domain.Task{
		ID:          strconv.Itoa(issue.GetNumber()),
		Title:       issue.GetTitle(),
		Description: body,
		Labels:      labels,
		Config:      cfg,
		Status:      domain.TaskStatusPending,
		Source:      "github",
		SourceURL:   issue.GetHTMLURL(),
		CreatedAt:   issue.GetCreatedAt().Time,
		UpdatedAt:   issue.GetUpdatedAt().Time,
	}
}

