package issueworkflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	charmlog "github.com/charmbracelet/log"

	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/proof"
	"github.com/haha-systems/ariadne/internal/provider"
	"github.com/haha-systems/ariadne/internal/router"
	"github.com/haha-systems/ariadne/internal/supervisor"
)

// mockSource is a hand-written fake worksource.WorkSource, extending the
// pattern used by internal/worksource/poller_test.go's mockSource: no
// mocking framework, just recorded calls.
type mockSource struct {
	postResults    []string
	postResultErr  error
	reviewOutcomes []reviewOutcome
	rebaseOutcomes []rebaseOutcome
}

type reviewOutcome struct {
	approved bool
	body     string
}

type rebaseOutcome struct {
	succeeded bool
	reason    string
}

func (m *mockSource) Name() string                                 { return "mock" }
func (m *mockSource) Poll(context.Context) ([]*domain.Task, error) { return nil, nil }
func (m *mockSource) Claim(context.Context, *domain.Task) error    { return nil }
func (m *mockSource) PostResult(_ context.Context, _ *domain.Task, summary string) error {
	m.postResults = append(m.postResults, summary)
	return m.postResultErr
}
func (m *mockSource) ListOpenPRs(context.Context) ([]*domain.Task, error) { return nil, nil }
func (m *mockSource) RecordRebaseOutcome(_ context.Context, _ *domain.Task, succeeded bool, reason string) error {
	m.rebaseOutcomes = append(m.rebaseOutcomes, rebaseOutcome{succeeded, reason})
	return nil
}
func (m *mockSource) ListPRsNeedingReview(context.Context) ([]*domain.Task, error)   { return nil, nil }
func (m *mockSource) ListPRsNeedingRevision(context.Context) ([]*domain.Task, error) { return nil, nil }
func (m *mockSource) RecordReviewOutcome(_ context.Context, _ *domain.Task, approved bool, body string) error {
	m.reviewOutcomes = append(m.reviewOutcomes, reviewOutcome{approved, body})
	return nil
}
func (m *mockSource) MarkPRNeedsReview(context.Context, int, int) error { return nil }

// stubProvider is a minimal fake provider.AgentProvider whose Wait behavior
// is scripted per test.
type stubProvider struct {
	name    string
	waitErr error
}

func (p stubProvider) Name() string                        { return p.name }
func (p stubProvider) Capabilities() []provider.Capability { return nil }
func (p stubProvider) CostEstimate(int) (float64, bool)    { return 0, false }
func (p stubProvider) Run(context.Context, provider.RunContext) (provider.RunHandle, error) {
	return stubHandle{err: p.waitErr}, nil
}

type stubHandle struct{ err error }

func (h stubHandle) Wait() error   { return h.err }
func (h stubHandle) Cancel() error { return nil }

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "init@test.com"},
		{"config", "user.name", "Init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func newTestWorkflow(source *mockSource, providers map[string]provider.AgentProvider, repoRoot string) *Workflow {
	rt := router.New(providers, map[string]string{}, "round-robin", "stub")
	sup := supervisor.New(supervisor.Config{
		RepoRoot:        repoRoot,
		WorktreeBaseDir: ".ariadne/runs",
		TimeoutMinutes:  1,
	})
	collector := proof.New(proof.Config{})
	return &Workflow{
		router:     rt,
		supervisor: sup,
		collector:  collector,
		source:     source,
	}
}

func TestExecuteTask_RoutingFailure_NoPostResult(t *testing.T) {
	source := &mockSource{}
	wf := newTestWorkflow(source, map[string]provider.AgentProvider{}, t.TempDir())

	task := &domain.Task{
		ID:     "task-1",
		Type:   domain.TaskTypeIssue,
		Config: &domain.TaskConfig{Agent: "does-not-exist"},
	}

	wf.executeTask(context.Background(), task)

	if len(source.postResults) != 0 {
		t.Errorf("expected no PostResult calls on routing failure, got %v", source.postResults)
	}
}

func TestExecuteRun_IssueTask_Success_PostsResultAndRunsHook(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	hookOut := filepath.Join(repoRoot, "hook-ran.txt")
	source := &mockSource{}
	wf := newTestWorkflow(source, nil, repoRoot)
	wf.hooks = []string{"cp \"$1\" " + hookOut}

	task := &domain.Task{ID: "task-1", Type: domain.TaskTypeIssue, Title: "test"}
	p := stubProvider{name: "stub"}

	wf.executeRun(context.Background(), task, p, nil, charmlog.With("test", true))

	if len(source.postResults) != 1 {
		t.Fatalf("expected exactly 1 PostResult call, got %d: %v", len(source.postResults), source.postResults)
	}
	if !strings.Contains(source.postResults[0], "task-1") {
		t.Errorf("expected proof summary to mention task ID, got %q", source.postResults[0])
	}
	if _, err := os.Stat(hookOut); err != nil {
		t.Errorf("expected hook to have run and produced %s: %v", hookOut, err)
	}
}

func TestExecuteRun_ReviewTask_Success_OutcomeAlreadyRecordedBySupervisor(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	source := &mockSource{}
	wf := newTestWorkflow(source, nil, repoRoot)

	task := &domain.Task{ID: "42", Type: domain.TaskTypeReview, SourceURL: "https://github.com/org/repo/pull/1"}
	p := stubProvider{name: "stub"}

	wf.executeRun(context.Background(), task, p, nil, charmlog.With("test", true))

	if len(source.postResults) != 0 {
		t.Errorf("expected no PostResult call for a review task (supervisor records the outcome directly), got %v", source.postResults)
	}
	if len(source.reviewOutcomes) != 1 || !source.reviewOutcomes[0].approved {
		t.Errorf("expected exactly one approved review outcome recorded by the supervisor, got %+v", source.reviewOutcomes)
	}
}

func TestExecuteRace_PicksWinnerAndPostsResult(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	source := &mockSource{}
	wf := newTestWorkflow(source, nil, repoRoot)

	task := &domain.Task{ID: "task-1", Type: domain.TaskTypeIssue, Title: "test"}
	route := router.RouteResult{
		Providers: []provider.AgentProvider{
			stubProvider{name: "winner"},
			stubProvider{name: "loser", waitErr: context.DeadlineExceeded},
		},
		RaceN: 2,
	}

	wf.executeRace(context.Background(), task, route, charmlog.With("test", true))

	if len(source.postResults) != 1 {
		t.Fatalf("expected exactly 1 PostResult call from the race winner, got %d", len(source.postResults))
	}
}
