package policy

// Invocation is the data passed to policy decisions (SelectRoute, PreRun, PostRun).
// This is the policy package's own view of an invocation (to avoid import cycles
// and to give us a stable surface for Starlark and future hooks).
//
// Many fields are mutable by PreRun policies (see StarlarkEngine docs for the
// full host API and mutation rules). ID and Source are intentionally protected
// from mutation by policy hooks.
type Invocation struct {
	ID        string
	Title     string
	Prompt    string
	Labels    []string
	Provider  string // explicit pin, if any (may be set/override by policy)
	Persona   string // explicit pin, if any (may be set/override by policy)
	Routing   string // explicit routing override, if any
	Source    string // e.g. "mcp", "discord", "cron" (protected from policy mutation)
	SourceURL string
	PublishMode string            // e.g. "required", "allowed", "skip" — may be mutated by PreRun
	Metadata  map[string]string // free-form; readable and writable by policy
	Env       map[string]string // additional environment vars (injected by PreRun for the executor)
}

// RouteDecision is what a policy engine returns from SelectRoute.
type RouteDecision struct {
	Provider      string
	Persona       string
	RaceProviders []string // if non-empty, run these in parallel (race)
}