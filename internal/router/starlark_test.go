package router

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/provider"
)

func TestRouter_Starlark(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "route.star")
	
	script := `
def route(task):
    if "urgent" in task["labels"]:
        return "gemini"
    if "slow" in task["labels"]:
        return {"provider": "claude", "persona": "senior-dev"}
    if "race" in task["labels"]:
        return {"providers": ["claude", "gemini"]}
    return None
`
	if err := os.WriteFile(scriptPath, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	providers := map[string]provider.AgentProvider{
		"claude": &starlarkStubProvider{name: "claude"},
		"gemini": &starlarkStubProvider{name: "gemini"},
	}
	personas := map[string]config.PersonaConfig{
		"senior-dev": {Name: "senior-dev", Provider: "claude"},
	}

	r := NewWithPersonas(providers, nil, nil, personas, "round-robin", "claude", scriptPath)

	// Test urgent -> gemini
	res, err := r.Route(&domain.Task{Labels: []string{"urgent"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Providers[0].Name() != "gemini" {
		t.Errorf("expected gemini, got %s", res.Providers[0].Name())
	}

	// Test slow -> claude + persona
	res, err = r.Route(&domain.Task{Labels: []string{"slow"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Providers[0].Name() != "claude" {
		t.Errorf("expected claude, got %s", res.Providers[0].Name())
	}
	if res.Persona == nil || res.Persona.Name != "senior-dev" {
		t.Errorf("expected senior-dev persona, got %v", res.Persona)
	}

	// Test race
	res, err = r.Route(&domain.Task{Labels: []string{"race"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Providers) != 2 {
		t.Errorf("expected 2 providers for race, got %d", len(res.Providers))
	}

	// Test fallback (None)
	res, err = r.Route(&domain.Task{Labels: []string{"none"}})
	if err != nil {
		t.Fatal(err)
	}
	// Default is round-robin, so it should pick claude or gemini (starts with claude)
	if res.Providers[0].Name() != "claude" {
		t.Errorf("expected fallback to claude, got %s", res.Providers[0].Name())
	}
}

type starlarkStubProvider struct {
	provider.AgentProvider
	name string
}

func (p *starlarkStubProvider) Name() string { return p.name }
func (p *starlarkStubProvider) CostEstimate(n int) (float64, bool) { return 0, false }
