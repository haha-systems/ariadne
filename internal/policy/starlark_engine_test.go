package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/haha-systems/ariadne/internal/memory"
)

func TestStarlarkEngine_SelectRoute(t *testing.T) {
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "policy.star")

	scriptContent := `
def select_route(inv):
    if "urgent" in inv["labels"]:
        return "gemini"
    if "slow" in inv["labels"]:
        return {"provider": "claude", "persona": "senior"}
    if "race" in inv["labels"]:
        return {"providers": ["claude", "gemini"]}
    return None
`
	if err := os.WriteFile(script, []byte(scriptContent), 0644); err != nil {
		t.Fatal(err)
	}

	eng, err := NewStarlarkEngine(script)
	if err != nil {
		t.Fatalf("NewStarlarkEngine: %v", err)
	}

	tests := []struct {
		name     string
		inv      Invocation
		wantProv string
		wantPers string
		wantRace int
	}{
		{
			name:     "urgent -> gemini",
			inv:      Invocation{Labels: []string{"urgent"}},
			wantProv: "gemini",
		},
		{
			name:     "slow -> claude + persona",
			inv:      Invocation{Labels: []string{"slow"}},
			wantProv: "claude",
			wantPers: "senior",
		},
		{
			name:     "race",
			inv:      Invocation{Labels: []string{"race"}},
			wantRace: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, err := eng.SelectRoute(context.Background(), tt.inv)
			if err != nil {
				t.Fatalf("SelectRoute: %v", err)
			}
			if dec == nil {
				t.Fatal("expected decision, got nil")
			}
			if tt.wantProv != "" && dec.Provider != tt.wantProv {
				t.Errorf("provider = %q, want %q", dec.Provider, tt.wantProv)
			}
			if tt.wantPers != "" && dec.Persona != tt.wantPers {
				t.Errorf("persona = %q, want %q", dec.Persona, tt.wantPers)
			}
			if tt.wantRace > 0 && len(dec.RaceProviders) != tt.wantRace {
				t.Errorf("race providers len = %d, want %d", len(dec.RaceProviders), tt.wantRace)
			}
		})
	}
}

// TestStarlarkEngine_PreRun_MutationAndVeto exercises the new PreRun wiring,
// rich invocation dict, and mutation sync for Starlark policies.
func TestStarlarkEngine_PreRun_MutationAndVeto(t *testing.T) {
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "policy.star")

	scriptContent := `
def pre_run(inv):
    # Demonstrate rich access + mutation
    if "veto" in inv.get("labels", []):
        fail("veto requested by policy for label")

    # rewrite prompt
    inv["prompt"] = "PRE: " + inv["prompt"]

    # inject env (new in Phase 3 T1) — merge with existing
    envd = inv.get("env") or {}
    envd["POLICY_INJECTED"] = "yes"
    envd["FOO"] = "42"
    inv["env"] = envd

    # mutate other fields
    inv["title"] = "mutated-title"
    prov = inv.get("provider") or ""
    if prov == "":
        inv["provider"] = "gemini-via-prerun"

    # labels append example
    inv["labels"] = (inv.get("labels") or []) + ["pre-run-applied"]

    # publish_mode mutation example
    inv["publish_mode"] = "allowed"

    log("pre_run completed successfully for " + inv["id"])
`
	if err := os.WriteFile(script, []byte(scriptContent), 0644); err != nil {
		t.Fatal(err)
	}

	eng, err := NewStarlarkEngine(script, StarlarkConfig{})
	if err != nil {
		t.Fatalf("NewStarlarkEngine: %v", err)
	}

	t.Run("mutation", func(t *testing.T) {
		inv := Invocation{
			ID:      "run_123",
			Title:   "orig",
			Prompt:  "do work",
			Source:  "test",
			Labels:  []string{"foo"},
			Env:     map[string]string{"EXISTING": "1"},
			Metadata: map[string]string{"meta1": "v1"},
		}
		err := eng.PreRun(context.Background(), &inv)
		if err != nil {
			t.Fatalf("PreRun: %v", err)
		}

		if inv.Prompt != "PRE: do work" {
			t.Errorf("prompt not rewritten: %q", inv.Prompt)
		}
		if inv.Title != "mutated-title" {
			t.Errorf("title: %q", inv.Title)
		}
		if inv.Provider != "gemini-via-prerun" {
			t.Errorf("provider: %q", inv.Provider)
		}
		if inv.PublishMode != "allowed" {
			t.Errorf("publish_mode: %q", inv.PublishMode)
		}
		if len(inv.Labels) != 2 || inv.Labels[1] != "pre-run-applied" {
			t.Errorf("labels: %v", inv.Labels)
		}
		if inv.Env["POLICY_INJECTED"] != "yes" || inv.Env["EXISTING"] != "1" {
			t.Errorf("env not properly merged/injected: %v", inv.Env)
		}
	})

	t.Run("veto", func(t *testing.T) {
		inv := Invocation{Labels: []string{"veto", "bar"}}
		err := eng.PreRun(context.Background(), &inv)
		if err == nil {
			t.Fatal("expected veto error from pre_run fail()")
		}
		if !contains(err.Error(), "veto requested") {
			t.Errorf("error should mention veto reason, got: %v", err)
		}
	})
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || len(sub) > 0 && (s[:len(sub)] == sub || contains(s[1:], sub))) }

// TestStarlarkEngine_Builtins exercises the new safe host API (json, log,
// read_file restricted, list_*, memory_*).
func TestStarlarkEngine_Builtins(t *testing.T) {
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "policy.star")

	// Create a safe file next to the script for read_file test.
	dataFile := filepath.Join(tmpDir, "data.txt")
	if err := os.WriteFile(dataFile, []byte("hello from policy read"), 0644); err != nil {
		t.Fatal(err)
	}

	scriptContent := `
def pre_run(inv):
    # json roundtrip
    obj = json.decode("{\"k\": 42}")
    if obj["k"] != 42:
        fail("json decode failed")
    enc = json.encode({"ok": True})
    if "ok" not in enc:
        fail("json encode failed")

    # log (just exercise; no assert on side effect)
    log("builtin test running for " + inv["id"])

    # read_file (sandboxed to script dir)
    content = read_file("data.txt")
    if content != "hello from policy read":
        fail("read_file content mismatch: " + content)

    # list_* (empty since no config passed, but must not crash)
    skills = list_skills()
    if type(skills) != "list":
        fail("list_skills must return list")
    if len(list_providers()) != 0:
        fail("expected empty providers")
    if len(list_personas()) != 0:
        fail("expected empty personas")

    # memory (no store configured -> graceful None / no-op)
    if memory_get("missing") != None:
        fail("memory_get without store should be None")
    memory_set("k", "v")  # must not error

    return None
`
	if err := os.WriteFile(script, []byte(scriptContent), 0644); err != nil {
		t.Fatal(err)
	}

	eng, err := NewStarlarkEngine(script)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	inv := Invocation{ID: "btest", Prompt: "x"}
	if err := eng.PreRun(context.Background(), &inv); err != nil {
		t.Fatalf("PreRun with builtins: %v", err)
	}
}

// TestStarlarkEngine_MemoryBuiltinsWithStore exercises memory_get/set when a real store is wired.
func TestStarlarkEngine_MemoryBuiltinsWithStore(t *testing.T) {
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "mem.star")
	memPath := filepath.Join(tmpDir, "mem.json")

	scriptContent := `
def pre_run(inv):
    memory_set("policy_key", "from-starlark", inv["id"])
    got = memory_get("policy_key")
    if got != "from-starlark":
        fail("memory roundtrip failed: " + str(got))
    return None
`
	if err := os.WriteFile(script, []byte(scriptContent), 0644); err != nil {
		t.Fatal(err)
	}

	store := memory.New(memPath)
	eng, err := NewStarlarkEngine(script, StarlarkConfig{Memory: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	inv := Invocation{ID: "memrun"}
	if err := eng.PreRun(context.Background(), &inv); err != nil {
		t.Fatalf("PreRun memory: %v", err)
	}

	// Verify persisted
	val, ok, err := store.Recall("policy_key")
	if err != nil || !ok || val != "from-starlark" {
		t.Errorf("store recall: val=%q ok=%v err=%v", val, ok, err)
	}
}
