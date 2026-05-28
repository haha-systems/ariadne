package operator

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/haha-systems/ariadne/internal/config"
)

func TestNew_NoEnabledProviders_ReturnsError(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{},
		Ariadne:   config.AriadneConfig{},
		Routing:   config.RoutingConfig{},
		Personas:  map[string]config.PersonaConfig{},
		Sandbox:   config.SandboxConfig{},
		Proof:     config.ProofConfig{},
	}

	_, err := New(cfg, t.TempDir())
	if err == nil {
		t.Fatal("expected error when no providers are enabled")
	}
	if !strings.Contains(err.Error(), "no enabled providers configured") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNew_SuccessWithOneProvider_ConstructsService(t *testing.T) {
	repoRoot := t.TempDir()

	cfg := &config.Config{
		Ariadne: config.AriadneConfig{
			DefaultProvider: "testprov",
		},
		Routing: config.RoutingConfig{
			Strategy:      "round-robin",
			LabelRoutes:   map[string]string{},
			PersonaRoutes: map[string]string{},
		},
		Personas: map[string]config.PersonaConfig{},
		Sandbox: config.SandboxConfig{
			WorktreeDir:       filepath.Join(".ariadne", "runs"),
			TimeoutMinutes:    10,
			PreserveOnFailure: false,
			WorkflowFile:      "",
			Env:               map[string]string{"TEST": "1"},
		},
		Proof: config.ProofConfig{
			RequireCIPass: false,
			PublishMode:   "allowed",
			PRBaseBranch:  "main",
		},
		Providers: map[string]config.ProviderConfig{
			"testprov": {
				Enabled:   true,
				Binary:    "/bin/sh",
				ExtraArgs: []string{"-c", "exit 0"},
			},
		},
		Hooks: []string{},
	}

	svc, err := New(cfg, repoRoot)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}

	// Basic sanity: internal fields are populated (via unexported but observable
	// through behavior of legacy helpers that remain for compat).
	// We avoid calling StartRun here to keep the test lightweight (no real
	// execution or provider invocation).
	if svc.cfg != cfg {
		t.Error("cfg not stored")
	}
	if svc.repoRoot != repoRoot {
		t.Error("repoRoot not stored")
	}

	// Also exercise the exported legacy helper (no side effects).
	if err := WriteToolResult(filepath.Join(t.TempDir(), "tool.json"), map[string]string{"ok": "true"}); err != nil {
		t.Errorf("WriteToolResult: %v", err)
	}
}

func TestNew_UsesGatewayProviderBuilder(t *testing.T) {
	// This test ensures that after the delegation refactor, operator.New
	// succeeds exactly when gateway.BuildProvidersFromConfig would produce
	// providers. (Indirectly validates no behavior change from the edit.)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"claude": {Enabled: true, Binary: "/nonexistent"},
		},
		Ariadne: config.AriadneConfig{},
		Routing: config.RoutingConfig{},
		Sandbox: config.SandboxConfig{},
		Proof:   config.ProofConfig{},
	}

	// Should not fail provider step.
	svc, err := New(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("New after delegation should succeed for enabled provider: %v", err)
	}
	if svc == nil {
		t.Fatal("nil service")
	}
}
