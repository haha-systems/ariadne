package policy

// Invocation is the data passed to policy decisions.
// This is the policy package's own view of an invocation (to avoid import cycles
// and to give us a stable surface for Starlark and future hooks).
type Invocation struct {
	ID        string
	Title     string
	Prompt    string
	Labels    []string
	Provider  string // explicit pin, if any
	Persona   string // explicit pin, if any
	Routing   string // explicit routing override, if any
	Source    string // e.g. "mcp", "discord", "cron"
	SourceURL string
	Metadata  map[string]string
}

// RouteDecision is what a policy engine returns from SelectRoute.
type RouteDecision struct {
	Provider      string
	Persona       string
	RaceProviders []string // if non-empty, run these in parallel (race)
}