package router

import (
	"fmt"
	"os"

	charmlog "github.com/charmbracelet/log"
	"go.starlark.net/starlark"

	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/provider"
)

// routeWithStarlark attempts to route a task using a Starlark script.
// It looks for a 'route' function in the script that takes a task dictionary.
func (r *Router) routeWithStarlark(scriptPath string, task *domain.Task) (RouteResult, bool, error) {
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return RouteResult{}, false, nil
	}

	thread := &starlark.Thread{Name: "ariadne-router"}
	globals, err := starlark.ExecFile(thread, scriptPath, nil, nil)
	if err != nil {
		return RouteResult{}, false, fmt.Errorf("starlark: exec %s: %w", scriptPath, err)
	}

	routeFunc, ok := globals["route"]
	if !ok {
		charmlog.Debug("starlark: 'route' function not found in script", "path", scriptPath)
		return RouteResult{}, false, nil
	}

	// Prepare task dictionary for Starlark.
	starlarkTask := starlark.NewDict(8)
	starlarkTask.SetKey(starlark.String("id"), starlark.String(task.ID))
	starlarkTask.SetKey(starlark.String("title"), starlark.String(task.Title))
	starlarkTask.SetKey(starlark.String("description"), starlark.String(task.Description))
	starlarkTask.SetKey(starlark.String("source"), starlark.String(task.Source))
	starlarkTask.SetKey(starlark.String("type"), starlark.String(string(task.Type)))

	labels := starlark.NewList(nil)
	for _, l := range task.Labels {
		labels.Append(starlark.String(l))
	}
	starlarkTask.SetKey(starlark.String("labels"), labels)

	if task.Config != nil {
		conf := starlark.NewDict(4)
		conf.SetKey(starlark.String("agent"), starlark.String(task.Config.Agent))
		conf.SetKey(starlark.String("persona"), starlark.String(task.Config.Persona))
		conf.SetKey(starlark.String("routing"), starlark.String(task.Config.Routing))
		starlarkTask.SetKey(starlark.String("config"), conf)
	}

	// Call the route function.
	args := starlark.Tuple{starlarkTask}
	res, err := starlark.Call(thread, routeFunc, args, nil)
	if err != nil {
		return RouteResult{}, true, fmt.Errorf("starlark: call 'route': %w", err)
	}

	return r.parseStarlarkResult(res)
}

func (r *Router) parseStarlarkResult(res starlark.Value) (RouteResult, bool, error) {
	switch v := res.(type) {
	case starlark.String:
		// Just a provider name.
		p, err := r.get(v.GoString())
		if err != nil {
			return RouteResult{}, true, err
		}
		return RouteResult{Providers: []provider.AgentProvider{p}, RaceN: 1}, true, nil

	case *starlark.Dict:
		// Dict with 'provider' and optionally 'persona', 'race'.
		var result RouteResult
		result.RaceN = 1

		providerVal, found, _ := v.Get(starlark.String("provider"))
		if found {
			if s, ok := providerVal.(starlark.String); ok {
				p, err := r.get(s.GoString())
				if err != nil {
					return RouteResult{}, true, err
				}
				result.Providers = []provider.AgentProvider{p}
			}
		}

		personaVal, found, _ := v.Get(starlark.String("persona"))
		if found {
			if s, ok := personaVal.(starlark.String); ok {
				if p, ok := r.personas[s.GoString()]; ok {
					result.Persona = &p
					// If persona has a provider and none was set explicitly, use it.
					if len(result.Providers) == 0 {
						pname := p.Provider
						if pname == "" {
							pname = r.defaultName
						}
						agent, err := r.get(pname)
						if err != nil {
							return RouteResult{}, true, err
						}
						result.Providers = []provider.AgentProvider{agent}
					}
				}
			}
		}

		raceVal, found, _ := v.Get(starlark.String("race"))
		if found {
			if n, err := starlark.AsInt32(raceVal); err == nil && n > 0 {
				result.RaceN = int(n)
			}
		}

		providersVal, found, _ := v.Get(starlark.String("providers"))
		if found {
			if list, ok := providersVal.(*starlark.List); ok {
				result.Providers = nil
				for i := 0; i < list.Len(); i++ {
					if s, ok := list.Index(i).(starlark.String); ok {
						p, err := r.get(s.GoString())
						if err != nil {
							return RouteResult{}, true, err
						}
						result.Providers = append(result.Providers, p)
					}
				}
				result.RaceN = len(result.Providers)
			}
		}

		if len(result.Providers) == 0 && result.Persona == nil {
			return RouteResult{}, false, nil // Let default routing take over.
		}
		return result, true, nil

	case starlark.NoneType:
		return RouteResult{}, false, nil

	default:
		return RouteResult{}, true, fmt.Errorf("starlark: 'route' returned unexpected type %T", res)
	}
}
