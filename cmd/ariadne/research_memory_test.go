package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/memory/inmemory"
)

// ghostdiveCfg points at a base URL that cannot serve requests, so
// preflight fails the way a misconfigured or unauthorized instance would.
func ghostdiveCfg() *config.Config {
	return &config.Config{
		Memory: config.MemoryConfig{
			Ghostdive: &config.GhostdiveConfig{
				BaseURL:          "http://127.0.0.1:1", // reserved, nothing listens
				SpaceIDByProject: map[string]string{"p1": "space-1"},
			},
		},
	}
}

func TestResolveResearchMemory_NoGhostdive_UsesInmemoryWithoutStrictness(t *testing.T) {
	var log bytes.Buffer
	store, strict, err := resolveResearchMemory(context.Background(), &config.Config{}, "p1", &log)
	if err != nil {
		t.Fatalf("resolveResearchMemory() error = %v", err)
	}
	if _, ok := store.(*inmemory.Store); !ok {
		t.Errorf("expected the in-memory backend, got %T", store)
	}
	if strict {
		t.Error("expected non-strict persistence for a backend that is inherently non-durable")
	}
}

// The 401 regression: a configured-but-unusable Ghostdive must stop the run
// before it starts, rather than letting every cycle's writes fail silently.
func TestResolveResearchMemory_GhostdiveUnreachable_FailsFastByDefault(t *testing.T) {
	t.Setenv("GHOSTDIVE_API_KEY", "test-key")
	var log bytes.Buffer

	_, _, err := resolveResearchMemory(context.Background(), ghostdiveCfg(), "p1", &log)
	if err == nil {
		t.Fatal("expected an unreachable Ghostdive to fail preflight by default")
	}
	if !strings.Contains(err.Error(), "allow_inmemory_fallback") {
		t.Errorf("expected the error to name the opt-in that would allow continuing, got %v", err)
	}
}

func TestResolveResearchMemory_MissingAPIKey_FailsFast(t *testing.T) {
	t.Setenv("GHOSTDIVE_API_KEY", "")
	var log bytes.Buffer

	_, _, err := resolveResearchMemory(context.Background(), ghostdiveCfg(), "p1", &log)
	if err == nil {
		t.Fatal("expected a missing GHOSTDIVE_API_KEY to fail before the run starts")
	}
	if !strings.Contains(err.Error(), "GHOSTDIVE_API_KEY") {
		t.Errorf("expected the error to name the missing variable, got %v", err)
	}
}

func TestResolveResearchMemory_ExplicitFallback_WarnsLoudlyAndContinues(t *testing.T) {
	t.Setenv("GHOSTDIVE_API_KEY", "test-key")
	cfg := ghostdiveCfg()
	cfg.Memory.AllowInmemoryFallback = true

	var log bytes.Buffer
	store, strict, err := resolveResearchMemory(context.Background(), cfg, "p1", &log)
	if err != nil {
		t.Fatalf("resolveResearchMemory() error = %v, want the explicit fallback to be honored", err)
	}
	if _, ok := store.(*inmemory.Store); !ok {
		t.Errorf("expected the fallback to be the in-memory backend, got %T", store)
	}
	if strict {
		t.Error("expected strict persistence to be off once the fallback is in use")
	}
	out := log.String()
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "LOST") {
		t.Errorf("expected a loud warning that research state will not survive, got %q", out)
	}
}
