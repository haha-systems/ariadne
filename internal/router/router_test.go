package router

import (
	"context"
	"testing"

	"github.com/haha-systems/conductor/internal/domain"
	"github.com/haha-systems/conductor/internal/provider"
)

type stubProvider struct {
	name            string
	costPer1kTokens float64
}

func (s *stubProvider) Name() string                      { return s.name }
func (s *stubProvider) Capabilities() []provider.Capability { return nil }
func (s *stubProvider) CostEstimate(promptLen int) (float64, bool) {
	if s.costPer1kTokens <= 0 {
		return 0, false
	}
	return (float64(promptLen) / 4000.0) * s.costPer1kTokens, true
}
func (s *stubProvider) Run(_ context.Context, _ provider.RunContext) (provider.RunHandle, error) {
	return nil, nil
}

func makeProviders(names ...string) map[string]provider.AgentProvider {
	m := make(map[string]provider.AgentProvider, len(names))
	for _, n := range names {
		m[n] = &stubProvider{name: n}
	}
	return m
}

func TestRouter_Pinned(t *testing.T) {
	r := New(makeProviders("claude", "codex"), nil, "round-robin", "claude")
	task := &domain.Task{Config: &domain.TaskConfig{Agent: "codex"}}
	result, err := r.Route(task)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Providers) != 1 || result.Providers[0].Name() != "codex" {
		t.Errorf("expected codex, got %v", result.Providers)
	}
}

func TestRouter_Pinned_UnknownProvider(t *testing.T) {
	r := New(makeProviders("claude"), nil, "round-robin", "claude")
	_, err := r.Route(&domain.Task{Config: &domain.TaskConfig{Agent: "nonexistent"}})
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestRouter_LabelBased(t *testing.T) {
	routes := map[string]string{"big-context": "gemini"}
	r := New(makeProviders("claude", "gemini"), routes, "round-robin", "claude")
	task := &domain.Task{Labels: []string{"conductor", "big-context"}}
	result, err := r.Route(task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Providers[0].Name() != "gemini" {
		t.Errorf("expected gemini, got %s", result.Providers[0].Name())
	}
}

func TestRouter_RoundRobin(t *testing.T) {
	r := New(makeProviders("a", "b"), nil, "round-robin", "a")
	task := &domain.Task{}
	seen := map[string]int{}
	for range 4 {
		result, err := r.Route(task)
		if err != nil {
			t.Fatal(err)
		}
		seen[result.Providers[0].Name()]++
	}
	if seen["a"] != 2 || seen["b"] != 2 {
		t.Errorf("expected even distribution, got %v", seen)
	}
}

func TestRouter_Cheapest(t *testing.T) {
	providers := map[string]provider.AgentProvider{
		"expensive": &stubProvider{name: "expensive", costPer1kTokens: 0.030},
		"cheap":     &stubProvider{name: "cheap", costPer1kTokens: 0.001},
		"mid":       &stubProvider{name: "mid", costPer1kTokens: 0.010},
	}
	r := New(providers, nil, "cheapest", "expensive")
	result, err := r.Route(&domain.Task{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Providers[0].Name() != "cheap" {
		t.Errorf("expected cheap provider, got %s", result.Providers[0].Name())
	}
}

func TestRouter_Cheapest_AllUnknown_FallsBackToRoundRobin(t *testing.T) {
	r := New(makeProviders("a", "b"), nil, "cheapest", "a")
	// stubProvider returns (0, false) when costPer1kTokens == 0
	result, err := r.Route(&domain.Task{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(result.Providers))
	}
}

func TestRouter_FrontMatterRouting_Cheapest(t *testing.T) {
	providers := map[string]provider.AgentProvider{
		"expensive": &stubProvider{name: "expensive", costPer1kTokens: 0.030},
		"cheap":     &stubProvider{name: "cheap", costPer1kTokens: 0.001},
	}
	r := New(providers, nil, "round-robin", "expensive")
	// Per-task front-matter overrides global strategy.
	task := &domain.Task{Config: &domain.TaskConfig{Routing: "cheapest"}}
	result, err := r.Route(task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Providers[0].Name() != "cheap" {
		t.Errorf("expected cheap, got %s", result.Providers[0].Name())
	}
}

func TestRouter_Race(t *testing.T) {
	r := New(makeProviders("a", "b", "c"), nil, "race 2", "a")
	result, err := r.Route(&domain.Task{})
	if err != nil {
		t.Fatal(err)
	}
	if result.RaceN != 2 {
		t.Errorf("expected RaceN=2, got %d", result.RaceN)
	}
	if len(result.Providers) != 2 {
		t.Errorf("expected 2 providers, got %d", len(result.Providers))
	}
	// Each selected provider should be distinct.
	if result.Providers[0].Name() == result.Providers[1].Name() {
		t.Errorf("race providers should be distinct, got %s and %s",
			result.Providers[0].Name(), result.Providers[1].Name())
	}
}

func TestRouter_Race_FewerProvidersThanN(t *testing.T) {
	r := New(makeProviders("a"), nil, "race 5", "a")
	result, err := r.Route(&domain.Task{})
	if err != nil {
		t.Fatal(err)
	}
	if result.RaceN != 1 {
		t.Errorf("expected RaceN capped at 1 (number of providers), got %d", result.RaceN)
	}
}

func TestRouter_Race_FrontMatter(t *testing.T) {
	r := New(makeProviders("a", "b", "c"), nil, "round-robin", "a")
	task := &domain.Task{Config: &domain.TaskConfig{Routing: "race 3"}}
	result, err := r.Route(task)
	if err != nil {
		t.Fatal(err)
	}
	if result.RaceN != 3 {
		t.Errorf("expected RaceN=3, got %d", result.RaceN)
	}
}

func TestRouter_Default(t *testing.T) {
	r := New(makeProviders("claude"), nil, "unknown-strategy", "claude")
	result, err := r.Route(&domain.Task{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Providers[0].Name() != "claude" {
		t.Errorf("expected claude, got %s", result.Providers[0].Name())
	}
}

func TestRouter_NoProviders(t *testing.T) {
	r := New(map[string]provider.AgentProvider{}, nil, "round-robin", "")
	_, err := r.Route(&domain.Task{})
	if err == nil {
		t.Error("expected error when no providers available")
	}
}
