package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/invocation"
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

// researchFixtureConfigWithRepo returns a full config equivalent to
// researchFixtureConfig but with [research.projects.demo] repo set —
// built from scratch rather than appended, since TOML doesn't allow
// redefining the same table twice in one document.
func researchFixtureConfigWithRepo(repoDir string) string {
	return `
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
repo = "` + repoDir + `"
routine_agent = "ari"
frontier_agent = "astra"
`
}

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

func TestBuildResearchLoop_RequiresRepo(t *testing.T) {
	t.Chdir(t.TempDir())
	cfgPath := writeTempConfig(t, researchFixtureConfig) // no repo set for [research.projects.demo]
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, _, err := buildResearchLoop(cfg, "demo"); err == nil {
		t.Fatal("expected an error when the research project has no repo configured")
	}
}

func TestBuildResearchLoop_WiresAriAndAstra(t *testing.T) {
	// buildResearchLoop writes its event log to a path relative to the
	// current working directory (.ariadne/research/<project>/run.jsonl) —
	// exactly what a real `ariadne research run` invocation wants (repo
	// root as cwd), but a test must not let that leak into the source tree.
	t.Chdir(t.TempDir())

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	cfgPath := writeTempConfig(t, researchFixtureConfigWithRepo(repoDir))
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

func TestBuildResearchLoop_ReusesWorkspaceAcrossCalls(t *testing.T) {
	t.Chdir(t.TempDir())

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	cfgPath := writeTempConfig(t, researchFixtureConfigWithRepo(repoDir))
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	loop1, cleanup1, err := buildResearchLoop(cfg, "demo")
	if err != nil {
		t.Fatalf("buildResearchLoop() error = %v", err)
	}
	ariRunner, ok := loop1.Ari.(invocation.ProviderRunner)
	if !ok {
		t.Fatalf("expected loop.Ari to be an invocation.ProviderRunner, got %T", loop1.Ari)
	}
	marker := filepath.Join(ariRunner.RepoPath, "notes-from-session-1.md")
	if err := os.WriteFile(marker, []byte("progress"), 0644); err != nil {
		t.Fatal(err)
	}
	cleanup1()

	_, cleanup2, err := buildResearchLoop(cfg, "demo")
	if err != nil {
		t.Fatalf("second buildResearchLoop() error = %v", err)
	}
	defer cleanup2()

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("expected the previous session's file to survive across buildResearchLoop calls: %v", err)
	}
}

func TestLoadResearchGuidance_MissingFileReturnsEmpty(t *testing.T) {
	t.Chdir(t.TempDir())
	proj := config.ResearchProjectConfig{RoutineAgent: "ari", FrontierAgent: "astra"}
	if got := loadResearchGuidance(proj, "demo"); got != "" {
		t.Errorf("loadResearchGuidance() = %q, want empty when no file exists", got)
	}
}

func TestLoadResearchGuidance_DefaultConventionPath(t *testing.T) {
	t.Chdir(t.TempDir())
	path := defaultGuidancePath("demo")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("Team mandate: characterize the parity effect."), 0644); err != nil {
		t.Fatal(err)
	}

	proj := config.ResearchProjectConfig{RoutineAgent: "ari", FrontierAgent: "astra"}
	got := loadResearchGuidance(proj, "demo")
	if got != "Team mandate: characterize the parity effect." {
		t.Errorf("loadResearchGuidance() = %q", got)
	}
}

func TestLoadResearchGuidance_ExplicitPathOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	explicitPath := filepath.Join(dir, "custom-guidance.md")
	if err := os.WriteFile(explicitPath, []byte("custom guidance"), 0644); err != nil {
		t.Fatal(err)
	}

	proj := config.ResearchProjectConfig{RoutineAgent: "ari", FrontierAgent: "astra", GuidanceFile: explicitPath}
	got := loadResearchGuidance(proj, "demo")
	if got != "custom guidance" {
		t.Errorf("loadResearchGuidance() = %q, want the explicit guidance_file content", got)
	}
}

func TestResearchGuidanceCmd_NoFileConfigured(t *testing.T) {
	t.Chdir(t.TempDir())
	cfgPath := writeTempConfig(t, researchFixtureConfig)
	cmd := researchGuidanceCmd(&cfgPath)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.RunE(cmd, []string{"demo"}); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if !strings.Contains(buf.String(), "No guidance file found") {
		t.Errorf("expected a no-guidance message, got %q", buf.String())
	}
}

func TestResearchGuidanceCmd_PrintsLoadedGuidance(t *testing.T) {
	t.Chdir(t.TempDir())
	guidancePath := defaultGuidancePath("demo")
	if err := os.MkdirAll(filepath.Dir(guidancePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guidancePath, []byte("Team mandate: characterize the parity effect."), 0644); err != nil {
		t.Fatal(err)
	}

	cfgPath := writeTempConfig(t, researchFixtureConfig)
	cmd := researchGuidanceCmd(&cfgPath)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.RunE(cmd, []string{"demo"}); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if !strings.Contains(buf.String(), "Team mandate: characterize the parity effect.") {
		t.Errorf("expected the loaded guidance text, got %q", buf.String())
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
