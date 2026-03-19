package worksource

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haha-systems/conductor/internal/domain"
)

// mockSource is a controllable WorkSource for testing.
type mockSource struct {
	tasks        []*domain.Task
	prTasks      []*domain.Task
	reviewTasks  []*domain.Task
	reviseTasks  []*domain.Task
	claimErr     error
	pollCount    atomic.Int32
	claimed      []string
	mu           sync.Mutex
}

func (m *mockSource) Name() string { return "mock" }

func (m *mockSource) Poll(_ context.Context) ([]*domain.Task, error) {
	m.pollCount.Add(1)
	return m.tasks, nil
}

func (m *mockSource) Claim(_ context.Context, task *domain.Task) error {
	if m.claimErr != nil {
		return m.claimErr
	}
	m.mu.Lock()
	m.claimed = append(m.claimed, task.ID)
	m.mu.Unlock()
	return nil
}

func (m *mockSource) PostResult(_ context.Context, _ *domain.Task, _ string) error {
	return nil
}

func (m *mockSource) ListOpenPRs(_ context.Context) ([]*domain.Task, error) {
	return m.prTasks, nil
}

func (m *mockSource) RecordRebaseOutcome(_ context.Context, _ *domain.Task, _ bool, _ string) error {
	return nil
}

func (m *mockSource) ListPRsNeedingReview(_ context.Context) ([]*domain.Task, error) {
	return m.reviewTasks, nil
}

func (m *mockSource) ListPRsNeedingRevision(_ context.Context) ([]*domain.Task, error) {
	return m.reviseTasks, nil
}

func (m *mockSource) RecordReviewOutcome(_ context.Context, _ *domain.Task, _ bool, _ string) error {
	return nil
}

func (m *mockSource) MarkPRNeedsReview(_ context.Context, _ int, _ int) error {
	return nil
}

func makeTasks(ids ...string) []*domain.Task {
	tasks := make([]*domain.Task, len(ids))
	for i, id := range ids {
		tasks[i] = &domain.Task{ID: id, Status: domain.TaskStatusPending}
	}
	return tasks
}

func TestPoller_ClaimsAvailableTasks(t *testing.T) {
	src := &mockSource{tasks: makeTasks("1", "2")}
	p := NewPoller(src, PollerConfig{IntervalSeconds: 60, MaxConcurrentRuns: 4})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := p.Run(ctx)

	var received []*domain.Task
	for task := range ch {
		received = append(received, task)
		p.Done()
		if len(received) == 2 {
			cancel()
		}
	}

	if len(received) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(received))
	}
	for _, task := range received {
		if task.Status != domain.TaskStatusClaimed {
			t.Errorf("task %s should be claimed", task.ID)
		}
	}
}

func TestPoller_BackPressure(t *testing.T) {
	src := &mockSource{tasks: makeTasks("1", "2", "3")}
	p := NewPoller(src, PollerConfig{IntervalSeconds: 60, MaxConcurrentRuns: 1})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := p.Run(ctx)

	// Only one slot — should receive only one task before blocking.
	task := <-ch
	if task == nil {
		t.Fatal("expected a task")
	}

	// Running count should be 1 (at capacity).
	if p.CurrentRunning() != 1 {
		t.Errorf("expected running=1, got %d", p.CurrentRunning())
	}

	cancel()
	// Drain
	for range ch {
	}
}

func TestPoller_RebaseAndIssueTasks_BothFlow(t *testing.T) {
	issueTasks := makeTasks("issue-1")
	prTask := &domain.Task{
		ID:     "pr-1",
		Status: domain.TaskStatusPending,
		Type:   domain.TaskTypeRebase,
	}
	src := &mockSource{
		tasks:   issueTasks,
		prTasks: []*domain.Task{prTask},
	}
	p := NewPoller(src, PollerConfig{IntervalSeconds: 60, MaxConcurrentRuns: 4})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := p.Run(ctx)
	var received []*domain.Task
	for task := range ch {
		received = append(received, task)
		p.Done()
		if len(received) == 2 {
			cancel()
		}
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 tasks (1 issue + 1 rebase), got %d", len(received))
	}

	types := map[domain.TaskType]bool{}
	for _, task := range received {
		types[task.Type] = true
	}
	if !types[domain.TaskTypeRebase] {
		t.Error("expected a rebase task to flow through the poller")
	}
}

func TestPoller_AllFourSources_Flow(t *testing.T) {
	issueTasks := makeTasks("issue-1")
	prTask := &domain.Task{ID: "pr-1", Status: domain.TaskStatusPending, Type: domain.TaskTypeRebase}
	reviewTask := &domain.Task{ID: "rev-1", Status: domain.TaskStatusPending, Type: domain.TaskTypeReview}
	reviseTask := &domain.Task{ID: "rvs-1", Status: domain.TaskStatusPending, Type: domain.TaskTypeRevise}

	src := &mockSource{
		tasks:       issueTasks,
		prTasks:     []*domain.Task{prTask},
		reviewTasks: []*domain.Task{reviewTask},
		reviseTasks: []*domain.Task{reviseTask},
	}
	p := NewPoller(src, PollerConfig{IntervalSeconds: 60, MaxConcurrentRuns: 4})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := p.Run(ctx)
	var received []*domain.Task
	for task := range ch {
		received = append(received, task)
		p.Done()
		if len(received) == 4 {
			cancel()
		}
	}

	if len(received) != 4 {
		t.Fatalf("expected 4 tasks (issue + rebase + review + revise), got %d", len(received))
	}

	types := map[domain.TaskType]int{}
	for _, task := range received {
		types[task.Type]++
	}
	if types[domain.TaskTypeReview] != 1 {
		t.Errorf("expected 1 review task, got %d", types[domain.TaskTypeReview])
	}
	if types[domain.TaskTypeRevise] != 1 {
		t.Errorf("expected 1 revise task, got %d", types[domain.TaskTypeRevise])
	}
}

func TestPoller_ClaimError_Skipped(t *testing.T) {
	src := &mockSource{
		tasks:    makeTasks("1"),
		claimErr: errors.New("already claimed"),
	}
	p := NewPoller(src, PollerConfig{IntervalSeconds: 60, MaxConcurrentRuns: 4})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch := p.Run(ctx)
	var count int
	for range ch {
		count++
	}

	if count != 0 {
		t.Errorf("expected 0 tasks when claim fails, got %d", count)
	}
}
