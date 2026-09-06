package agentprofile

import (
	"reflect"
	"testing"
)

func TestRegistry_Get(t *testing.T) {
	ari := Profile{Name: "ari", CostClass: CostCheap, ScarcityClass: ScarcityPlentiful}
	astra := Profile{Name: "astra", CostClass: CostExpensive, ScarcityClass: ScarcityRare}
	r := NewRegistry(map[string]Profile{"ari": ari, "astra": astra})

	got, err := r.Get("ari")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Name != "ari" {
		t.Errorf("Get(ari).Name = %q, want ari", got.Name)
	}

	if _, err := r.Get("nonexistent"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestRegistry_Names_SortedDeterministic(t *testing.T) {
	r := NewRegistry(map[string]Profile{
		"zeta":  {Name: "zeta"},
		"alpha": {Name: "alpha"},
		"mid":   {Name: "mid"},
	})
	want := []string{"alpha", "mid", "zeta"}
	if got := r.Names(); !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
}

func TestRegistry_ByCostClass(t *testing.T) {
	r := NewRegistry(map[string]Profile{
		"ari":      {Name: "ari", CostClass: CostCheap},
		"mid-tier": {Name: "mid-tier", CostClass: CostStandard},
		"astra":    {Name: "astra", CostClass: CostExpensive},
	})

	cases := []struct {
		max  CostClass
		want []string
	}{
		{CostCheap, []string{"ari"}},
		{CostStandard, []string{"ari", "mid-tier"}},
		{CostExpensive, []string{"ari", "astra", "mid-tier"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.max), func(t *testing.T) {
			got := r.ByCostClass(tc.max)
			names := make([]string, len(got))
			for i, p := range got {
				names[i] = p.Name
			}
			if !reflect.DeepEqual(names, tc.want) {
				t.Errorf("ByCostClass(%s) = %v, want %v", tc.max, names, tc.want)
			}
		})
	}
}

func TestRegistry_ByCostClass_UnknownClass(t *testing.T) {
	r := NewRegistry(map[string]Profile{"ari": {Name: "ari", CostClass: CostCheap}})
	if got := r.ByCostClass(CostClass("bogus")); got != nil {
		t.Errorf("ByCostClass(bogus) = %v, want nil", got)
	}
}
