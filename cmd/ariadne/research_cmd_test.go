package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haha-systems/ariadne/internal/config"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ariadne.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

const researchFixtureConfig = `
[ariadne]
default_provider = "codex"

[providers.codex]
enabled = true
binary = "echo"

[agent_profiles.ari]
provider = "codex"
cost_class = "cheap"
scarcity_class = "plentiful"

[agent_profiles.astra]
provider = "codex"
cost_class = "expensive"
scarcity_class = "rare"

[research.projects.demo]
routine_agent = "ari"
frontier_agent = "astra"
`

func TestAgentsCmd_ListsConfiguredProfiles(t *testing.T) {
	cfgPath := writeTempConfig(t, researchFixtureConfig)
	cmd := agentsCmd(&cfgPath)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	out := buf.String()
	if !containsAll(out, "ari", "astra", "cheap", "expensive", "plentiful", "rare") {
		t.Errorf("agents output missing expected fields: %q", out)
	}
}

func TestBudgetCmd_PrintsConfiguredLimits(t *testing.T) {
	cfgPath := writeTempConfig(t, researchFixtureConfig+"\n[qac]\napproval = \"manual\"\nmax_calls_per_cycle = 1\n")
	cmd := budgetCmd(&cfgPath)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	out := buf.String()
	if !containsAll(out, "manual", "max_calls_per_cycle:   1") {
		t.Errorf("budget output missing expected fields: %q", out)
	}
}

func TestResearchFrontierCmd_NoFrontierYet(t *testing.T) {
	cfgPath := writeTempConfig(t, researchFixtureConfig)
	cmd := researchFrontierCmd(&cfgPath)
	cmd.SetArgs([]string{"demo"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error when no frontier has been recorded yet")
	}
}

func TestBuildResearchLoop_UnknownProject(t *testing.T) {
	cfgPath := writeTempConfig(t, researchFixtureConfig)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, _, err := buildResearchLoop(cfg, "does-not-exist"); err == nil {
		t.Fatal("expected an error for an unconfigured research project")
	}
}

func TestBuildResearchLoop_WiresAriAndAstra(t *testing.T) {
	// buildResearchLoop writes its event log to a path relative to the
	// current working directory (.ariadne/research/<project>/run.jsonl) —
	// exactly what a real `ariadne research run` invocation wants (repo
	// root as cwd), but a test must not let that leak into the source tree.
	t.Chdir(t.TempDir())

	cfgPath := writeTempConfig(t, researchFixtureConfig)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	loop, cleanup, err := buildResearchLoop(cfg, "demo")
	if err != nil {
		t.Fatalf("buildResearchLoop() error = %v", err)
	}
	defer cleanup()

	if loop.AriProfileName != "ari" || loop.AstraProfileName != "astra" {
		t.Errorf("unexpected profile names: ari=%q astra=%q", loop.AriProfileName, loop.AstraProfileName)
	}
	if loop.Ari == nil || loop.Astra == nil || loop.Memory == nil || loop.Decider == nil {
		t.Error("expected all AriLoop dependencies to be wired")
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}
