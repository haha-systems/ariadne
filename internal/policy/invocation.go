package policy

// Invocation is the data passed to policy decisions (SelectRoute, PreRun, PostRun).
// This is the policy package's own view of an invocation (to avoid import cycles
// and to give us a stable surface for Starlark and future hooks).
//
// PreRun mutation semantics (applies to both Go Engine implementations and
// Starlark pre_run hooks):
//
//   - ID and Source are read-only: they are never mutated by policy and are
//     ignored by apply logic in the gateway.
//   - All other fields are mutable. Assignment (including setting a string to
//     the empty value "") overwrites the field on the invocation passed to the
//     executor and recorded in the run. This is the unified rule: policy authors
//     may clear fields by writing the zero value.
//   - To intentionally leave a field unchanged, a Go PreRun impl must not
//     write to the field on the *Invocation; a Starlark pre_run must not assign
//     to the corresponding key in the inv dict (the original value remains).
//   - Labels, Metadata, and Env are replaced entirely with the value provided
//     by the policy (use empty slice or nil/empty map to clear).
//   - Setting Provider/Persona to "" clears any pin (later defaults may apply
//     depending on context). Setting Title/Prompt to "" clears them.
//
// See applyPolicyInvToGateway (gateway package) and StarlarkEngine for
// concrete application and the host API.
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