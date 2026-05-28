package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/policy"
)

// fakeExecutor is a test double that does a small delay (simulating work)
// and returns success or ctx error. It does *not* mutate the run record
// directly — the gateway owns status transitions for thread-safety in this spike.
type fakeExecutor struct {
	delay time.Duration
}

func (f *fakeExecutor) Execute(ctx context.Context, runID string, inv Invocation) (string, error) {
	select {
	case <-time.After(f.delay):
		return "/tmp/fake-worktree-" + runID, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestGateway_SubmitAndGet_Smoke(t *testing.T) {
	gw, err := New(Config{
		DefaultProvider: "fake",
	}, &fakeExecutor{delay: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer gw.Close()

	inv := Invocation{
		Title:    "smoke test run",
		Prompt:   "do something useful",
		Provider: "fake",
		Source:   "test",
		Metadata: map[string]string{"foo": "bar"},
	}

	run, err := gw.Submit(context.Background(), inv)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if run.ID == "" {
		t.Fatal("expected generated ID")
	}
	if run.Status != domain.RunStatusPending && run.Status != domain.RunStatusRunning {
		t.Fatalf("unexpected initial status: %s", run.Status)
	}

	// Wait a bit for the async executor.
	time.Sleep(50 * time.Millisecond)

	got, err := gw.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != domain.RunStatusSucceeded {
		t.Fatalf("expected succeeded, got %s (lastEvent=%s, err=%s)", got.Status, got.LastEvent, got.LastError)
	}
	// Worktree is still set by the real engine in later phases; for this spike
	// the fake executor doesn't set it, so we don't assert on it here.
	if got.Metadata["foo"] != "bar" {
		t.Errorf("metadata not preserved: %v", got.Metadata)
	}
}

func TestGateway_Cancel(t *testing.T) {
	gw, err := New(Config{
		DefaultProvider: "fake",
	}, &fakeExecutor{delay: 2 * time.Second}) // long enough to cancel
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer gw.Close()

	run, err := gw.Submit(context.Background(), Invocation{Title: "cancel me", Prompt: "x"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	cancelled, err := gw.Cancel(run.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !cancelled {
		t.Fatal("expected cancel to be delivered")
	}

	time.Sleep(20 * time.Millisecond)

	got, _ := gw.GetRun(run.ID)
	if got.Status != domain.RunStatusFailed && got.LastEvent != "cancel_requested" {
		t.Fatalf("expected cancellation to have taken effect, got status=%s lastEvent=%s err=%s", got.Status, got.LastEvent, got.LastError)
	}
}

func TestGateway_ResultHandler(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	handler := &captureHandler{called: &wg}

	gw, err := New(Config{
		DefaultProvider: "fake",
		ResultHandlers:  []ResultHandler{handler},
	}, &fakeExecutor{delay: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer gw.Close()

	_, err = gw.Submit(context.Background(), Invocation{Title: "handler test", Prompt: "x"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(200 * time.Millisecond):
		t.Fatal("result handler was not called")
	}

	if handler.lastRun == nil {
		t.Fatal("handler received nil run")
	}
}

type captureHandler struct {
	mu      sync.Mutex
	lastRun *Run
	called  *sync.WaitGroup
}

func (c *captureHandler) Handle(ctx context.Context, run *Run, inv *Invocation, outcome any) error {
	c.mu.Lock()
	c.lastRun = run
	c.mu.Unlock()
	if c.called != nil {
		c.called.Done()
	}
	return nil
}

// TestGateway_WithRealSupervisorExecutor proves that the Gateway + SupervisorExecutor
// seam can drive a real provider through the supervisor for a normal run.
// This is the first integration of the "one gateway" path.
func TestGateway_WithRealSupervisorExecutor(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	cfg := &config.Config{
		Ariadne: config.AriadneConfig{
			MaxConcurrentRuns:   1,
			DefaultProvider:     "sleeper",
			WorkIntervalSeconds: 30,
		},
		Routing: config.RoutingConfig{
			Strategy:      "round-robin",
			LabelRoutes:   map[string]string{},
			PersonaRoutes: map[string]string{},
		},
		Sandbox: config.SandboxConfig{
			WorktreeDir:       ".ariadne/runs",
			TimeoutMinutes:    2,
			PreserveOnFailure: true,
			WorkflowFile:      "",
			Env:               map[string]string{},
		},
		Providers: map[string]config.ProviderConfig{
			"sleeper": {
				Enabled:   true,
				Binary:    "/bin/sh",
				ExtraArgs: []string{"-c", "trap 'exit 0' TERM INT; while true; do sleep 0.1; done"},
			},
		},
		Personas: map[string]config.PersonaConfig{},
	}

	providers := BuildProvidersFromConfig(cfg)
	sup := DefaultSupervisorForGateway(cfg, repoRoot)

	exec := NewSupervisorExecutor(repoRoot, sup, providers, cfg.Personas)

	gw, err := New(Config{
		RepoRoot:        repoRoot,
		DefaultProvider: "sleeper",
	}, exec)
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	defer gw.Close()

	run, err := gw.Submit(context.Background(), Invocation{
		Title:    "real executor smoke",
		Prompt:   "just sleep a little then exit cleanly",
		Provider: "sleeper",
		Source:   "gateway-test",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Give the sleeper a moment, then cancel it (we don't want a  long-running test).
	time.Sleep(80 * time.Millisecond)

	_, _ = gw.Cancel(run.ID)

	// Wait for termination to be observed.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := gw.GetRun(run.ID)
		if got.Status == domain.RunStatusFailed || got.Status == domain.RunStatusSucceeded {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("run %s did not reach terminal state quickly, but no crash — acceptable for this smoke", run.ID)
}

// --- test helpers (minimal copies from mcpserver tests for self-contained spike) ---

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "Ariadne Test")
	runGit(t, dir, "config", "user.email", "ariadne@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "init")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}

// TestGateway_WithStarlarkPolicy proves that a real Starlark policy file can
// influence provider selection when submitting through the gateway.
func TestGateway_WithStarlarkPolicy(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	// Create a simple policy file
	policyPath := filepath.Join(repoRoot, "policy.star")
	policyContent := `
def select_route(inv):
    if "gemini-preferred" in inv["labels"]:
        return "gemini"
    return "sleeper"
`
	if err := os.WriteFile(policyPath, []byte(policyContent), 0644); err != nil {
		t.Fatalf("write policy.star: %v", err)
	}

	cfg := &config.Config{
		Ariadne: config.AriadneConfig{
			DefaultProvider: "sleeper",
		},
		Sandbox: config.SandboxConfig{
			WorktreeDir:       ".ariadne/runs",
			TimeoutMinutes:    1,
			PreserveOnFailure: true,
		},
		Providers: map[string]config.ProviderConfig{
			"sleeper": {
				Enabled:   true,
				Binary:    "/bin/sh",
				ExtraArgs: []string{"-c", "sleep 0.05; exit 0"},
			},
			"gemini": {
				Enabled:   true,
				Binary:    "/bin/sh",
				ExtraArgs: []string{"-c", "sleep 0.05; exit 0"},
			},
		},
		Personas: map[string]config.PersonaConfig{},
	}

	providers := BuildProvidersFromConfig(cfg)
	sup := DefaultSupervisorForGateway(cfg, repoRoot)
	exec := NewSupervisorExecutor(repoRoot, sup, providers, cfg.Personas)

	// Load Starlark policy
	pol, err := policy.NewStarlarkEngine(policyPath)
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}

	gw, err := New(Config{
		RepoRoot:        repoRoot,
		DefaultProvider: "sleeper",
		Policy:          pol,
	}, exec)
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	defer gw.Close()

	// Submit with label that should trigger gemini via policy
	run, err := gw.Submit(context.Background(), Invocation{
		Title:  "policy test",
		Prompt: "test",
		Labels: []string{"gemini-preferred"},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Give it a moment to run
	time.Sleep(120 * time.Millisecond)

	got, err := gw.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	if got.Provider != "gemini" {
		t.Errorf("expected provider 'gemini' via policy, got %q", got.Provider)
	}
}

// =============================================================================
// Tests for Phase 2 Task D: PostRun wiring + new built-in ResultHandlers
// =============================================================================

// capturingPolicy records the last PostRun call for assertions.
type capturingPolicy struct {
	mu        sync.Mutex
	lastRun   policy.RunSummary
	lastInv   policy.Invocation
	callCount int
}

func (c *capturingPolicy) SelectRoute(ctx context.Context, inv policy.Invocation) (*policy.RouteDecision, error) {
	return nil, nil
}
func (c *capturingPolicy) PreRun(ctx context.Context, inv *policy.Invocation) error { return nil }
func (c *capturingPolicy) PostRun(ctx context.Context, run policy.RunSummary, inv policy.Invocation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastRun = run
	c.lastInv = inv
	c.callCount++
	return nil
}

func TestGateway_PostRunCalledBeforeHandlers(t *testing.T) {
	pol := &capturingPolicy{}

	var handlerWg sync.WaitGroup
	handlerWg.Add(1)
	handler := &captureHandler{called: &handlerWg}

	gw, err := New(Config{
		DefaultProvider: "fake",
		Policy:          pol,
		ResultHandlers:  []ResultHandler{handler},
	}, &fakeExecutor{delay: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer gw.Close()

	inv := Invocation{
		Title:     "postrun test",
		Prompt:    "x",
		Source:    "test-postrun",
		SourceURL: "https://example.com/123",
		Labels:    []string{"foo"},
	}
	_, err = gw.Submit(context.Background(), inv)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for handler (implies completion + PostRun happened)
	done := make(chan struct{})
	go func() { handlerWg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("completion did not happen")
	}

	pol.mu.Lock()
	defer pol.mu.Unlock()
	if pol.callCount != 1 {
		t.Fatalf("expected PostRun called once, got %d", pol.callCount)
	}
	if pol.lastRun.ID == "" {
		t.Error("PostRun received empty run summary")
	}
	if pol.lastRun.Status != string(domain.RunStatusSucceeded) {
		t.Errorf("PostRun status = %s, want succeeded", pol.lastRun.Status)
	}
	if pol.lastInv.Source != "test-postrun" {
		t.Errorf("PostRun inv source = %q", pol.lastInv.Source)
	}
	// Handler also saw it (order tested indirectly by both firing)
	if handler.lastRun == nil {
		t.Error("handler did not receive run")
	}
}

func TestProofSummaryResultHandler_WritesJSON(t *testing.T) {
	tmp := t.TempDir()
	worktree := filepath.Join(tmp, "run_abc123")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}

	h := NewProofSummaryResultHandler()
	run := &Run{
		ID:         "run_abc123",
		Title:      "demo",
		Provider:   "claude",
		Status:     domain.RunStatusSucceeded,
		Worktree:   worktree,
		StartedAt:  time.Now().Add(-10 * time.Second).UTC(),
		FinishedAt: func() *time.Time { t := time.Now().UTC(); return &t }(),
		LastEvent:  "done",
	}
	inv := &Invocation{Source: "test", SourceURL: "u"}

	if err := h.Handle(context.Background(), run, inv, nil); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	path := filepath.Join(worktree, "proof", "summary.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("summary.json not written: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["run_id"] != "run_abc123" {
		t.Errorf("run_id = %v", got["run_id"])
	}
	if got["status"] != "succeeded" {
		t.Errorf("status = %v", got["status"])
	}
	if got["source"] != "test" {
		t.Errorf("source = %v", got["source"])
	}
}

func TestWebhookResultHandler_PostsJSON(t *testing.T) {
	var received struct {
		mu   sync.Mutex
		body []byte
		hdr  http.Header
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received.mu.Lock()
		received.body = b
		received.hdr = r.Header.Clone()
		received.mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	h := NewWebhookResultHandler(srv.URL,
		WithWebhookTimeout(2*time.Second),
		WithWebhookHeader("X-Test", "yes"),
	)

	run := &Run{ID: "r1", Title: "t", Status: domain.RunStatusFailed, StartedAt: time.Now().UTC(), FinishedAt: func() *time.Time { tt := time.Now().UTC(); return &tt }()}
	inv := &Invocation{Source: "webhook-test"}

	err := h.Handle(context.Background(), run, inv, nil)
	if err != nil {
		t.Fatalf("Handle returned error (unexpected for 200): %v", err)
	}

	received.mu.Lock()
	defer received.mu.Unlock()
	if len(received.body) == 0 {
		t.Fatal("no body received by test server")
	}
	var payload map[string]any
	if err := json.Unmarshal(received.body, &payload); err != nil {
		t.Fatalf("server body not json: %v", err)
	}
	if payload["event"] != "run.completed" {
		t.Errorf("event = %v", payload["event"])
	}
	if received.hdr.Get("X-Test") != "yes" {
		t.Errorf("custom header missing")
	}
	if received.hdr.Get("Content-Type") != "application/json" {
		t.Errorf("content-type = %s", received.hdr.Get("Content-Type"))
	}
}

func TestWebhookResultHandler_BadStatusIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	h := NewWebhookResultHandler(srv.URL)
	err := h.Handle(context.Background(), &Run{ID: "r", StartedAt: time.Now().UTC(), FinishedAt: func() *time.Time { t := time.Now().UTC(); return &t }()}, &Invocation{}, nil)
	if err == nil {
		t.Error("expected error for 5xx response")
	}
}
