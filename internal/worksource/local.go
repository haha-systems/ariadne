package worksource

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/haha-systems/ariadne/internal/domain"
)

// Local is a work source for tasks typed straight into the harness, with no
// issue tracker in front of them.
//
// It exists so that starting work from the interface reuses the whole
// pipeline — routing, worktree isolation, supervision, proof — rather than
// growing a second, less careful way to run an agent. The only things it
// changes are where a task comes from (a person, right now) and where its
// result goes (back to the caller, since there is no issue to comment on).
//
// It implements the PR-lifecycle halves of WorkSource as no-ops: an ad-hoc
// task has no pull request to rebase or review. They return empty rather
// than an error, because "there is nothing to review here" is the correct
// answer, not a failure.
type Local struct {
	mu      sync.Mutex
	pending []*domain.Task
	results map[string]string

	// onResult is notified when a task's summary arrives, so the harness
	// can surface it on the session without polling for it.
	onResult func(task *domain.Task, summary string)

	seq int
}

// NewLocal constructs an empty local source. onResult may be nil.
func NewLocal(onResult func(task *domain.Task, summary string)) *Local {
	return &Local{results: make(map[string]string), onResult: onResult}
}

// Name implements TaskSource.
func (l *Local) Name() string { return "local" }

// Submit queues an ad-hoc task and returns it. The first line of the prompt
// becomes the title, so the session list reads as a list of intentions
// rather than a list of identifiers.
func (l *Local) Submit(prompt string) (*domain.Task, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("worksource: empty task prompt")
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.seq++
	now := time.Now()
	task := &domain.Task{
		ID:          fmt.Sprintf("local-%d", l.seq),
		Title:       firstLine(prompt),
		Description: prompt,
		Status:      domain.TaskStatusPending,
		Source:      "local",
		Type:        domain.TaskTypeIssue,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	l.pending = append(l.pending, task)
	return task, nil
}

// Poll implements TaskSource, draining everything submitted since the last
// call. Draining rather than re-reporting is what keeps a task from being
// claimed twice when the poller ticks faster than a run finishes.
func (l *Local) Poll(context.Context) ([]*domain.Task, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.pending) == 0 {
		return nil, nil
	}
	out := l.pending
	l.pending = nil
	return out, nil
}

// Claim implements TaskSource. A local task has exactly one claimant by
// construction — it was handed to this process directly — so claiming is
// bookkeeping rather than coordination.
func (l *Local) Claim(_ context.Context, task *domain.Task) error {
	if task == nil {
		return fmt.Errorf("worksource: claiming a nil task")
	}
	task.Status = domain.TaskStatusClaimed
	task.UpdatedAt = time.Now()
	return nil
}

// PostResult implements TaskSource by recording the summary and notifying
// the harness, since there is no issue to comment on.
func (l *Local) PostResult(_ context.Context, task *domain.Task, summary string) error {
	if task == nil {
		return fmt.Errorf("worksource: posting a result for a nil task")
	}

	l.mu.Lock()
	l.results[task.ID] = summary
	onResult := l.onResult
	l.mu.Unlock()

	if onResult != nil {
		onResult(task, summary)
	}
	return nil
}

// Result returns the recorded summary for a task, if one has arrived.
func (l *Local) Result(taskID string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	summary, ok := l.results[taskID]
	return summary, ok
}

// ListOpenPRs implements RebaseSource: an ad-hoc task has no pull request.
func (l *Local) ListOpenPRs(context.Context) ([]*domain.Task, error) { return nil, nil }

// RecordRebaseOutcome implements RebaseSource as a no-op.
func (l *Local) RecordRebaseOutcome(context.Context, *domain.Task, bool, string) error { return nil }

// ListPRsNeedingReview implements ReviewSource: nothing here is in review.
func (l *Local) ListPRsNeedingReview(context.Context) ([]*domain.Task, error) { return nil, nil }

// ListPRsNeedingRevision implements ReviewSource as an empty list.
func (l *Local) ListPRsNeedingRevision(context.Context) ([]*domain.Task, error) { return nil, nil }

// RecordReviewOutcome implements ReviewSource as a no-op.
func (l *Local) RecordReviewOutcome(context.Context, *domain.Task, bool, string) error { return nil }

// MarkPRNeedsReview implements ReviewSource as a no-op.
func (l *Local) MarkPRNeedsReview(context.Context, int, int) error { return nil }

// firstLine is the task's title: the first non-empty line, trimmed to
// something that fits a list row.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 72 {
			return line[:69] + "..."
		}
		return line
	}
	return s
}

var _ WorkSource = (*Local)(nil)
