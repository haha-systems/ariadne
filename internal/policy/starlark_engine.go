package policy

import (
	"context"
	"fmt"
	"os"

	charmlog "github.com/charmbracelet/log"
	"go.starlark.net/starlark"
)

// StarlarkEngine loads a Starlark policy file and uses it to make decisions.
// It looks for functions such as:
//   - select_route(inv) -> str | dict | None
//
// This is the beginning of generalizing the old router/starlark logic into
// the new gateway policy system.
type StarlarkEngine struct {
	scriptPath string
}

// NewStarlarkEngine creates an engine backed by the given Starlark file.
func NewStarlarkEngine(scriptPath string) (*StarlarkEngine, error) {
	if _, err := os.Stat(scriptPath); err != nil {
		return nil, fmt.Errorf("policy starlark file not found: %s: %w", scriptPath, err)
	}
	return &StarlarkEngine{scriptPath: scriptPath}, nil
}

func (e *StarlarkEngine) SelectRoute(ctx context.Context, inv Invocation) (*RouteDecision, error) {
	thread := &starlark.Thread{Name: "ariadne-policy"}

	globals, err := starlark.ExecFile(thread, e.scriptPath, nil, starlarkPredeclared())
	if err != nil {
		return nil, fmt.Errorf("starlark: exec %s: %w", e.scriptPath, err)
	}

	fn, ok := globals["select_route"]
	if !ok {
		// Fall back to old name for compatibility during migration
		fn, ok = globals["route"]
	}
	if !ok {
		charmlog.Debug("starlark policy: no select_route or route function found", "path", e.scriptPath)
		return nil, nil
	}

	starInv := invocationToStarlark(inv)

	res, err := starlark.Call(thread, fn, starlark.Tuple{starInv}, nil)
	if err != nil {
		return nil, fmt.Errorf("starlark: call select_route: %w", err)
	}

	return parseRouteDecision(res)
}

// PreRun and PostRun are not yet implemented for Starlark in this step.
func (e *StarlarkEngine) PreRun(ctx context.Context, inv *Invocation) error {
	return nil
}

func (e *StarlarkEngine) PostRun(ctx context.Context, run RunSummary, inv Invocation) error {
	return nil
}

// --- helpers ---

func starlarkPredeclared() starlark.StringDict {
	// Start minimal and expand over time (json is very useful)
	return starlark.StringDict{
		"json": jsonModule(),
	}
}

func invocationToStarlark(inv Invocation) *starlark.Dict {
	d := starlark.NewDict(10)
	d.SetKey(starlark.String("id"), starlark.String(inv.ID))
	d.SetKey(starlark.String("title"), starlark.String(inv.Title))
	d.SetKey(starlark.String("prompt"), starlark.String(inv.Prompt))
	d.SetKey(starlark.String("source"), starlark.String(inv.Source))
	d.SetKey(starlark.String("source_url"), starlark.String(inv.SourceURL))

	labels := starlark.NewList(nil)
	for _, l := range inv.Labels {
		labels.Append(starlark.String(l))
	}
	d.SetKey(starlark.String("labels"), labels)

	if inv.Provider != "" {
		d.SetKey(starlark.String("provider"), starlark.String(inv.Provider))
	}
	if inv.Persona != "" {
		d.SetKey(starlark.String("persona"), starlark.String(inv.Persona))
	}
	if inv.Routing != "" {
		d.SetKey(starlark.String("routing"), starlark.String(inv.Routing))
	}

	return d
}

func parseRouteDecision(v starlark.Value) (*RouteDecision, error) {
	switch val := v.(type) {
	case starlark.NoneType:
		return nil, nil

	case starlark.String:
		return &RouteDecision{Provider: val.GoString()}, nil

	case *starlark.Dict:
		dec := &RouteDecision{}

		if p, found, _ := val.Get(starlark.String("provider")); found {
			if s, ok := p.(starlark.String); ok {
				dec.Provider = s.GoString()
			}
		}
		if p, found, _ := val.Get(starlark.String("persona")); found {
			if s, ok := p.(starlark.String); ok {
				dec.Persona = s.GoString()
			}
		}
		if providers, found, _ := val.Get(starlark.String("providers")); found {
			if list, ok := providers.(*starlark.List); ok {
				for i := 0; i < list.Len(); i++ {
					if s, ok := list.Index(i).(starlark.String); ok {
						dec.RaceProviders = append(dec.RaceProviders, s.GoString())
					}
				}
			}
		}
		return dec, nil

	default:
		return nil, fmt.Errorf("policy: unexpected return type from select_route: %T", v)
	}
}

// jsonModule returns a very small json module (encode/decode) for Starlark policies.
// This is intentionally limited compared to a full json package.
func jsonModule() starlark.Value {
	// For the first deeper step we provide a stub.
	// A real implementation can use go.starlark.net/starlarkjson or similar.
	return starlark.NewDict(0) // placeholder - policies can still work without it for routing
}