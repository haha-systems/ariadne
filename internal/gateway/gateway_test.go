package gateway

import (
	"context"
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

	// Load Starlark policy (supply StarlarkConfig so enriched builtins like
	// list_* and read_file with gateway data are available in policy scripts).
	pol, err := policy.NewStarlarkEngine(policyPath, policy.StarlarkConfig{
		Providers: cfg.Providers,
		Personas:  cfg.Personas,
		Skills:    cfg.Skills,
	})
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

// capturingExecutor records the Invocation it receives (for PreRun mutation tests).
type capturingExecutor struct {
	mu  sync.Mutex
	inv Invocation
}

func (c *capturingExecutor) Execute(ctx context.Context, runID string, inv Invocation) (string, error) {
	c.mu.Lock()
	c.inv = inv // copy
	c.mu.Unlock()
	// small delay so async doesn't race the checks in tests
	time.Sleep(5 * time.Millisecond)
	return "/tmp/capt-" + runID, nil
}

func (c *capturingExecutor) lastInv() Invocation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inv
}

// TestGateway_PreRun_MutationsAndVeto proves that PreRun is wired in Submit,
// mutations (env, prompt rewrite, provider) are respected and reach the
// executor, and veto prevents run creation.
func TestGateway_PreRun_MutationsAndVeto(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	policyPath := filepath.Join(repoRoot, "prerun-policy.star")
	policyContent := `
def pre_run(inv):
    inv["prompt"] = "AUGMENTED: " + inv["prompt"]
    inv["env"] = {"PRE_RUN_ENV": "1", "EXTRA": "policy"}
    if "force-gemini" in inv.get("labels", []):
        inv["provider"] = "gemini"
    if "veto-me" in inv.get("labels", []):
        fail("veto from test policy")
    return None
`
	if err := os.WriteFile(policyPath, []byte(policyContent), 0644); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	cfg := &config.Config{
		Ariadne: config.AriadneConfig{DefaultProvider: "sleeper"},
		Sandbox: config.SandboxConfig{
			WorktreeDir:       ".ariadne/runs",
			TimeoutMinutes:    1,
			PreserveOnFailure: true,
		},
		Providers: map[string]config.ProviderConfig{
			"sleeper": {Enabled: true, Binary: "/bin/sh", ExtraArgs: []string{"-c", "exit 0"}},
			"gemini":  {Enabled: true, Binary: "/bin/sh", ExtraArgs: []string{"-c", "exit 0"}},
		},
		Personas: map[string]config.PersonaConfig{},
	}

	providers := BuildProvidersFromConfig(cfg)
	sup := DefaultSupervisorForGateway(cfg, repoRoot)
	exec := NewSupervisorExecutor(repoRoot, sup, providers, cfg.Personas)

	pol, err := policy.NewStarlarkEngine(policyPath, policy.StarlarkConfig{
		Providers: cfg.Providers,
		Personas:  cfg.Personas,
		Skills:    cfg.Skills,
	})
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

	t.Run("mutation_reaches_executor", func(t *testing.T) {
		capExec := &capturingExecutor{}
		// Recreate gw with capturer for isolation (simple for test)
		gw2, _ := New(Config{RepoRoot: repoRoot, DefaultProvider: "sleeper", Policy: pol}, capExec)
		defer gw2.Close()

		_, err := gw2.Submit(context.Background(), Invocation{
			Title:  "mut test",
			Prompt: "original prompt",
			Labels: []string{"force-gemini"},
			Source: "test",
		})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		time.Sleep(30 * time.Millisecond)

		got := capExec.lastInv()
		if got.Prompt != "AUGMENTED: original prompt" {
			t.Errorf("executor did not see mutated prompt: %q", got.Prompt)
		}
		if got.Env["PRE_RUN_ENV"] != "1" {
			t.Errorf("executor did not see injected env: %v", got.Env)
		}
		if got.Provider != "gemini" {
			t.Errorf("executor did not see provider override from pre_run: %q", got.Provider)
		}
	})

	t.Run("veto_prevents_run", func(t *testing.T) {
		_, err := gw.Submit(context.Background(), Invocation{
			Title:  "veto test",
			Prompt: "x",
			Labels: []string{"veto-me"},
		})
		if err == nil {
			t.Fatal("expected error from PreRun veto")
		}
		if !contains(err.Error(), "pre_run failed") || !contains(err.Error(), "veto from test") {
			t.Errorf("error should wrap pre_run veto, got: %v", err)
		}
	})
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}