package worksource_test

import (
	"context"
	"strings"
	"testing"

	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/worksource"
)

func TestLocalSubmitAndPoll(t *testing.T) {
	l := worksource.NewLocal(nil)
	ctx := context.Background()

	if _, err := l.Submit("   "); err == nil {
		t.Fatal("Submit with an empty prompt succeeded, want an error")
	}

	task, err := l.Submit("fix the login redirect\n\nmore detail here")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if task.Title != "fix the login redirect" {
		t.Fatalf("Title = %q, want the first line of the prompt", task.Title)
	}
	if !strings.Contains(task.Description, "more detail here") {
		t.Fatal("Description lost the body of the prompt")
	}
	if task.Type != domain.TaskTypeIssue {
		t.Fatalf("Type = %q, want an issue-shaped task", task.Type)
	}

	got, err := l.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(got) != 1 || got[0].ID != task.ID {
		t.Fatalf("Poll = %+v, want the submitted task", got)
	}

	// Draining is what stops a task being claimed twice when the poller
	// ticks faster than a run finishes.
	again, err := l.Poll(ctx)
	if err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second Poll returned %d tasks, want the queue drained", len(again))
	}
}

func TestLocalPostResultRecordsAndNotifies(t *testing.T) {
	var gotTask *domain.Task
	var gotSummary string
	l := worksource.NewLocal(func(task *domain.Task, summary string) {
		gotTask, gotSummary = task, summary
	})

	task, err := l.Submit("add retry backoff")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := l.PostResult(context.Background(), task, "all tests pass"); err != nil {
		t.Fatalf("PostResult: %v", err)
	}

	if gotTask == nil || gotTask.ID != task.ID || gotSummary != "all tests pass" {
		t.Fatalf("callback got (%v, %q), want the task and its summary", gotTask, gotSummary)
	}
	if summary, ok := l.Result(task.ID); !ok || summary != "all tests pass" {
		t.Fatalf("Result = (%q, %v), want the recorded summary", summary, ok)
	}
}

func TestLocalClaimMarksTheTask(t *testing.T) {
	l := worksource.NewLocal(nil)
	task, err := l.Submit("do a thing")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := l.Claim(context.Background(), task); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if task.Status != domain.TaskStatusClaimed {
		t.Fatalf("Status = %q, want claimed", task.Status)
	}
	if err := l.Claim(context.Background(), nil); err == nil {
		t.Fatal("Claim(nil) succeeded, want an error")
	}
}

// An ad-hoc task has no pull request, so the PR-lifecycle surfaces answer
// "nothing here" rather than failing.
func TestLocalHasNoPRLifecycle(t *testing.T) {
	l := worksource.NewLocal(nil)
	ctx := context.Background()

	if prs, err := l.ListOpenPRs(ctx); err != nil || len(prs) != 0 {
		t.Fatalf("ListOpenPRs = (%v, %v), want empty and no error", prs, err)
	}
	if prs, err := l.ListPRsNeedingReview(ctx); err != nil || len(prs) != 0 {
		t.Fatalf("ListPRsNeedingReview = (%v, %v), want empty and no error", prs, err)
	}
	if prs, err := l.ListPRsNeedingRevision(ctx); err != nil || len(prs) != 0 {
		t.Fatalf("ListPRsNeedingRevision = (%v, %v), want empty and no error", prs, err)
	}
	if err := l.RecordReviewOutcome(ctx, nil, true, ""); err != nil {
		t.Fatalf("RecordReviewOutcome: %v", err)
	}
	if err := l.RecordRebaseOutcome(ctx, nil, true, ""); err != nil {
		t.Fatalf("RecordRebaseOutcome: %v", err)
	}
	if err := l.MarkPRNeedsReview(ctx, 1, 2); err != nil {
		t.Fatalf("MarkPRNeedsReview: %v", err)
	}
}
