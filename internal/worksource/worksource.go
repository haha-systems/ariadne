package worksource

import (
	"context"

	"github.com/haha-systems/ariadne/internal/domain"
)

// TaskSource is the minimal contract a work source must satisfy to
// participate in the plain issue workflow: discover tasks, claim them, and
// report a result back. Sources that have no PR-lifecycle concept (e.g.
// Linear) need implement nothing more than this.
type TaskSource interface {
	// Name returns the source identifier (e.g. "github", "linear").
	Name() string
	// Poll returns pending tasks from this source.
	Poll(ctx context.Context) ([]*domain.Task, error)
	// Claim atomically marks a task as claimed in the source system.
	// Returns an error if the task was already claimed by another process.
	Claim(ctx context.Context, task *domain.Task) error
	// PostResult posts a proof summary comment back to the originating task/issue.
	PostResult(ctx context.Context, task *domain.Task, summary string) error
}

// RebaseSource is implemented by work sources that support the automated PR
// rebase lifecycle (currently GitHub only).
type RebaseSource interface {
	// ListOpenPRs returns open PRs that are behind their base branch and
	// eligible for an automated rebase (not already claimed or abandoned).
	ListOpenPRs(ctx context.Context) ([]*domain.Task, error)
	// RecordRebaseOutcome updates the source system after a rebase attempt.
	// On success, removes the in-progress marker. On failure, increments the
	// attempt counter and, after 3 failures, marks the PR as abandoned and
	// posts a comment explaining why.
	RecordRebaseOutcome(ctx context.Context, task *domain.Task, succeeded bool, reason string) error
}

// ReviewSource is implemented by work sources that support the QA
// review/revise PR lifecycle (currently GitHub only).
type ReviewSource interface {
	// ListPRsNeedingReview returns open PRs labelled ariadne:needs-review that
	// are not already being reviewed or terminal (approved, abandoned).
	ListPRsNeedingReview(ctx context.Context) ([]*domain.Task, error)
	// ListPRsNeedingRevision returns open PRs labelled ariadne:needs-revision
	// that are not already being revised or terminal (abandoned).
	ListPRsNeedingRevision(ctx context.Context) ([]*domain.Task, error)
	// RecordReviewOutcome updates labels after a QA review completes.
	// On approval, adds ariadne:approved and removes in-progress labels.
	// On rejection, increments the cycle counter and, after 3 cycles, marks
	// the PR as review-abandoned and posts an abandonment comment.
	RecordReviewOutcome(ctx context.Context, task *domain.Task, approved bool, body string) error
	// MarkPRNeedsReview adds ariadne:needs-review to the PR and records the
	// originating issue number as a ariadne:issue-N label.
	MarkPRNeedsReview(ctx context.Context, prNumber int, issueNumber int) error
}

// WorkSource is the full interface the issue workflow depends on. It is
// composed of the narrower TaskSource/RebaseSource/ReviewSource interfaces
// so that future workflows (e.g. research) can depend on just the piece
// they need without inheriting PR-lifecycle semantics that don't apply to
// them.
type WorkSource interface {
	TaskSource
	RebaseSource
	ReviewSource
}
