package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
