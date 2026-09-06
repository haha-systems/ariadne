// Package agentprofile separates cognitive-agent identity (which model,
// what it costs, how scarce it is) from provider identity (which CLI
// executes it). A Profile always names a provider.AgentProvider, but the
// scheduler and research workflow only ever reason about CostClass and
// ScarcityClass — never about the underlying model string — so model names
// stay confined to configuration and provider adapters.
package agentprofile

import (
	"fmt"
	"sort"

	"github.com/haha-systems/ariadne/internal/provider"
)

// CostClass buckets an agent by how expensive it is to run.
type CostClass string

const (
	CostCheap     CostClass = "cheap"
	CostStandard  CostClass = "standard"
	CostExpensive CostClass = "expensive"
)

// costRank orders CostClass values from least to most expensive, for
// ByCostClass's "at most this expensive" filtering.
var costRank = map[CostClass]int{
	CostCheap:     0,
	CostStandard:  1,
	CostExpensive: 2,
}

// ScarcityClass buckets an agent by how constrained its quota is.
type ScarcityClass string

const (
	ScarcityPlentiful ScarcityClass = "plentiful"
	ScarcityLimited   ScarcityClass = "limited"
	ScarcityRare      ScarcityClass = "rare"
)

// Profile is a configured cognitive resource: a provider plus the
// scheduling-relevant metadata that lets the scheduler pick between agents
// without knowing model names or provider internals.
type Profile struct {
	Name          string
	Provider      provider.AgentProvider
	CostClass     CostClass
	ScarcityClass ScarcityClass
	// ModelOverride is passed as provider.RunContext.ModelOverride on every
	// invocation through this profile. Ignored by providers that don't
	// implement provider.ModelOverrideSupporter.
	ModelOverride string
	SystemPrompt  string
}

// Registry looks up configured profiles by name.
type Registry struct {
	profiles map[string]Profile
	order    []string // sorted names, for deterministic iteration/listing
}

// NewRegistry builds a Registry from a name->Profile map.
func NewRegistry(profiles map[string]Profile) *Registry {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	stored := make(map[string]Profile, len(profiles))
	for _, name := range names {
		stored[name] = profiles[name]
	}
	return &Registry{profiles: stored, order: names}
}

// Get returns the named profile, or an error if it isn't registered.
func (r *Registry) Get(name string) (Profile, error) {
	p, ok := r.profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("agentprofile: unknown profile %q", name)
	}
	return p, nil
}

// Names returns all registered profile names in sorted order.
func (r *Registry) Names() []string {
	return append([]string(nil), r.order...)
}

// ByCostClass returns all profiles at or below the given CostClass, in
// registry order. An unrecognized max returns nil.
func (r *Registry) ByCostClass(max CostClass) []Profile {
	maxRank, ok := costRank[max]
	if !ok {
		return nil
	}
	var out []Profile
	for _, name := range r.order {
		p := r.profiles[name]
		if rank, ok := costRank[p.CostClass]; ok && rank <= maxRank {
			out = append(out, p)
		}
	}
	return out
}
