package policy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/haha-systems/ariadne/internal/config"
	"github.com/haha-systems/ariadne/internal/memory"
	charmlog "github.com/charmbracelet/log"
	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

// StarlarkConfig supplies gateway context to StarlarkEngine so that the
// enriched host API (list_*, memory_*, restricted read_file) can be
// useful and safe. Zero values are safe: lists return empty, memory ops
// are graceful no-ops, and read_file defaults to the policy script directory.
type StarlarkConfig struct {
	// AllowedReadRoots are absolute or relative base directories that
	// read_file is permitted to read from (including subdirectories).
	// Relative roots are resolved at construction time against the process cwd.
	// If empty, NewStarlarkEngine defaults to the directory containing scriptPath.
	AllowedReadRoots []string

	// Snapshots of gateway configuration for the list_* builtins.
	// These are the same maps the gateway itself uses.
	Providers map[string]config.ProviderConfig
	Personas  map[string]config.PersonaConfig
	Skills    map[string]config.SkillConfig

	// Memory is the persistent memory store for memory_get / memory_set.
	// If nil the builtins are graceful no-ops (get returns None, set is ignored).
	Memory *memory.Store
}

// StarlarkEngine loads a Starlark policy file and uses it to make decisions
// via select_route (or legacy route) and pre_run hooks.
//
// It implements the full Engine interface and provides a powerful, safe
// Starlark host API (predeclared names) modeled on the CLI plugin pattern
// but hardened for policy use inside the gateway.
type StarlarkEngine struct {
	scriptPath   string
	allowedRoots []string
	providers    map[string]config.ProviderConfig
	personas     map[string]config.PersonaConfig
	skills       map[string]config.SkillConfig
	memory       *memory.Store
}

// NewStarlarkEngine creates an engine backed by the given Starlark file.
//
// The optional cfg (variadic for backward compat) enriches the builtins
// available to policy scripts. See StarlarkConfig docs.
func NewStarlarkEngine(scriptPath string, cfg ...StarlarkConfig) (*StarlarkEngine, error) {
	if _, err := os.Stat(scriptPath); err != nil {
		return nil, fmt.Errorf("policy starlark file not found: %s: %w", scriptPath, err)
	}

	var c StarlarkConfig
	if len(cfg) > 0 {
		c = cfg[0]
	}

	roots := c.AllowedReadRoots
	if len(roots) == 0 {
		roots = []string{filepath.Dir(scriptPath)}
	}
	// Normalize roots once
	normRoots := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			abs = r
		}
		normRoots = append(normRoots, abs)
	}
	if len(normRoots) == 0 {
		normRoots = []string{filepath.Dir(scriptPath)}
	}

	return &StarlarkEngine{
		scriptPath:   scriptPath,
		allowedRoots: normRoots,
		providers:    c.Providers,
		personas:     c.Personas,
		skills:       c.Skills,
		memory:       c.Memory,
	}, nil
}

func (e *StarlarkEngine) SelectRoute(ctx context.Context, inv Invocation) (*RouteDecision, error) {
	thread := &starlark.Thread{Name: "ariadne-policy"}

	globals, err := starlark.ExecFile(thread, e.scriptPath, nil, e.predeclared())
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

// PreRun executes the policy's pre_run(inv) hook (if defined) after routing
// but before executor invocation.
//
// The inv dict passed to pre_run is mutable from Starlark. Assignments to
// supported keys (prompt, title, provider, persona, routing, labels, env,
// metadata, publish_mode, source_url) are applied back to the Go Invocation
// after the call returns.
//
// Veto: the hook can call fail("human readable reason") (or any Starlark
// error) to abort the run before it is persisted or launched. The error is
// wrapped and returned so the gateway can surface it cleanly.
//
// If no pre_run symbol exists, this is a silent no-op (consistent with
// the documented Engine contract).
func (e *StarlarkEngine) PreRun(ctx context.Context, inv *Invocation) error {
	if inv == nil {
		return fmt.Errorf("policy: nil invocation passed to PreRun")
	}

	thread := &starlark.Thread{Name: "ariadne-policy-prerun"}
	thread.Print = func(_ *starlark.Thread, msg string) {
		charmlog.Debug("starlark policy", "print", msg)
	}

	globals, err := starlark.ExecFile(thread, e.scriptPath, nil, e.predeclared())
	if err != nil {
		return fmt.Errorf("starlark: exec %s: %w", e.scriptPath, err)
	}

	fn, ok := globals["pre_run"]
	if !ok {
		// No pre_run defined — allowed and common. No-op.
		return nil
	}

	starInv := invocationToStarlark(*inv)

	_, err = starlark.Call(thread, fn, starlark.Tuple{starInv}, nil)
	if err != nil {
		// Any error (including fail() from the script) is a veto.
		return fmt.Errorf("pre_run veto: %w", err)
	}

	// Apply mutations made by the Starlark script (in-place dict changes).
	if err := updateInvocationFromStarlark(starInv, inv); err != nil {
		return fmt.Errorf("policy: applying pre_run mutations: %w", err)
	}
	return nil
}

func (e *StarlarkEngine) PostRun(ctx context.Context, run RunSummary, inv Invocation) error {
	return nil
}

// --- helpers ---

func (e *StarlarkEngine) predeclared() starlark.StringDict {
	pd := starlark.StringDict{
		"json": json.Module,
	}

	// log(msg) — safe debugging for policy authors. Uses charm logger at debug.
	pd["log"] = starlark.NewBuiltin("log", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var msg string
		if err := starlark.UnpackArgs("log", args, kwargs, "msg", &msg); err != nil {
			return starlark.None, err
		}
		charmlog.Debug("starlark policy log", "msg", msg)
		return starlark.None, nil
	})

	// read_file(path) — restricted to allowed roots (defaults to script dir).
	pd["read_file"] = starlark.NewBuiltin("read_file", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var path string
		if err := starlark.UnpackArgs("read_file", args, kwargs, "path", &path); err != nil {
			return starlark.None, err
		}
		abs, err := e.safeResolveReadPath(path)
		if err != nil {
			return starlark.None, err
		}
		// Size guard: keep policy reads cheap and prevent accidental huge files.
		const maxRead = 512 * 1024
		fi, err := os.Stat(abs)
		if err == nil && fi.Size() > maxRead {
			return starlark.None, fmt.Errorf("read_file: file too large (%d bytes > %d limit)", fi.Size(), maxRead)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return starlark.None, fmt.Errorf("read_file %q: %w", path, err)
		}
		return starlark.String(string(data)), nil
	})

	// memory_get(key) -> str | None
	pd["memory_get"] = starlark.NewBuiltin("memory_get", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var key string
		if err := starlark.UnpackArgs("memory_get", args, kwargs, "key", &key); err != nil {
			return starlark.None, err
		}
		if e.memory == nil {
			return starlark.None, nil
		}
		val, ok, err := e.memory.Recall(key)
		if err != nil {
			return starlark.None, fmt.Errorf("memory_get: %w", err)
		}
		if !ok {
			return starlark.None, nil
		}
		return starlark.String(val), nil
	})

	// memory_set(key, value, run_id=None)
	pd["memory_set"] = starlark.NewBuiltin("memory_set", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var key, value string
		var runID starlark.Value = starlark.None
		if err := starlark.UnpackArgs("memory_set", args, kwargs, "key", &key, "value", &value, "run_id?", &runID); err != nil {
			return starlark.None, err
		}
		if e.memory == nil {
			return starlark.None, nil
		}
		rid := ""
		if s, ok := runID.(starlark.String); ok {
			rid = s.GoString()
		}
		if err := e.memory.Remember(key, value, rid); err != nil {
			return starlark.None, fmt.Errorf("memory_set: %w", err)
		}
		return starlark.None, nil
	})

	// list_* return simple lists of names (high-signal, low surface area).
	// Richer metadata can be obtained via read_file on persona/skill dirs when needed.
	pd["list_skills"] = starlark.NewBuiltin("list_skills", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if err := starlark.UnpackArgs("list_skills", args, kwargs); err != nil {
			return starlark.None, err
		}
		l := starlark.NewList(nil)
		for name := range e.skills {
			l.Append(starlark.String(name))
		}
		return l, nil
	})

	pd["list_providers"] = starlark.NewBuiltin("list_providers", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if err := starlark.UnpackArgs("list_providers", args, kwargs); err != nil {
			return starlark.None, err
		}
		l := starlark.NewList(nil)
		for name := range e.providers {
			l.Append(starlark.String(name))
		}
		return l, nil
	})

	pd["list_personas"] = starlark.NewBuiltin("list_personas", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if err := starlark.UnpackArgs("list_personas", args, kwargs); err != nil {
			return starlark.None, err
		}
		l := starlark.NewList(nil)
		for name := range e.personas {
			l.Append(starlark.String(name))
		}
		return l, nil
	})

	return pd
}

func invocationToStarlark(inv Invocation) *starlark.Dict {
	d := starlark.NewDict(16)
	d.SetKey(starlark.String("id"), starlark.String(inv.ID))
	d.SetKey(starlark.String("title"), starlark.String(inv.Title))
	d.SetKey(starlark.String("prompt"), starlark.String(inv.Prompt))
	d.SetKey(starlark.String("source"), starlark.String(inv.Source))
	d.SetKey(starlark.String("source_url"), starlark.String(inv.SourceURL))
	d.SetKey(starlark.String("publish_mode"), starlark.String(inv.PublishMode))

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

	if len(inv.Metadata) > 0 {
		meta := starlark.NewDict(len(inv.Metadata))
		for k, v := range inv.Metadata {
			meta.SetKey(starlark.String(k), starlark.String(v))
		}
		d.SetKey(starlark.String("metadata"), meta)
	}

	if len(inv.Env) > 0 {
		env := starlark.NewDict(len(inv.Env))
		for k, v := range inv.Env {
			env.SetKey(starlark.String(k), starlark.String(v))
		}
		d.SetKey(starlark.String("env"), env)
	}

	return d
}

// updateInvocationFromStarlark reads supported mutable keys out of the
// (potentially mutated) Starlark dict and writes them back into inv.
// ID and Source are deliberately ignored (protected).
func updateInvocationFromStarlark(d *starlark.Dict, inv *Invocation) error {
	if d == nil || inv == nil {
		return nil
	}

	getStr := func(key string) (string, bool) {
		if v, found, _ := d.Get(starlark.String(key)); found {
			if s, ok := v.(starlark.String); ok {
				return s.GoString(), true
			}
		}
		return "", false
	}

	if s, ok := getStr("title"); ok {
		inv.Title = s
	}
	if s, ok := getStr("prompt"); ok {
		inv.Prompt = s
	}
	if s, ok := getStr("provider"); ok {
		inv.Provider = s
	}
	if s, ok := getStr("persona"); ok {
		inv.Persona = s
	}
	if s, ok := getStr("routing"); ok {
		inv.Routing = s
	}
	if s, ok := getStr("source_url"); ok {
		inv.SourceURL = s
	}
	if s, ok := getStr("publish_mode"); ok {
		inv.PublishMode = s
	}

	// labels list
	if v, found, _ := d.Get(starlark.String("labels")); found {
		if lst, ok := v.(*starlark.List); ok {
			inv.Labels = make([]string, 0, lst.Len())
			for i := 0; i < lst.Len(); i++ {
				if s, ok := lst.Index(i).(starlark.String); ok {
					inv.Labels = append(inv.Labels, s.GoString())
				}
			}
		}
	}

	// env dict (string->string only)
	if v, found, _ := d.Get(starlark.String("env")); found {
		if dict, ok := v.(*starlark.Dict); ok {
			inv.Env = make(map[string]string)
			for _, keyVal := range dict.Keys() {
				if ks, ok := keyVal.(starlark.String); ok {
					k := ks.GoString()
					if val, found, _ := dict.Get(ks); found {
						if vs, ok := val.(starlark.String); ok {
							inv.Env[k] = vs.GoString()
						}
					}
				}
			}
		}
	}

	// metadata dict (string->string only)
	if v, found, _ := d.Get(starlark.String("metadata")); found {
		if dict, ok := v.(*starlark.Dict); ok {
			inv.Metadata = make(map[string]string)
			for _, keyVal := range dict.Keys() {
				if ks, ok := keyVal.(starlark.String); ok {
					k := ks.GoString()
					if val, found, _ := dict.Get(ks); found {
						if vs, ok := val.(starlark.String); ok {
							inv.Metadata[k] = vs.GoString()
						}
					}
				}
			}
		}
	}

	return nil
}

// safeResolveReadPath implements the sandbox for the read_file builtin.
// Relative paths are resolved against the first allowed root (normally the
// directory of the policy.star file). Absolute paths and paths with .. that
// escape the roots are rejected. Called from the predeclared closure.
func (e *StarlarkEngine) safeResolveReadPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("read_file: empty path")
	}
	if strings.Contains(p, "\x00") {
		return "", fmt.Errorf("read_file: invalid path")
	}

	clean := filepath.Clean(p)

	var candidate string
	if filepath.IsAbs(clean) {
		candidate = clean
	} else if len(e.allowedRoots) > 0 {
		candidate = filepath.Join(e.allowedRoots[0], clean)
	} else {
		return "", fmt.Errorf("read_file: relative path %q has no allowed base directory", p)
	}
	candidate = filepath.Clean(candidate)

	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("read_file: resolving %q: %w", p, err)
	}

	for _, root := range e.allowedRoots {
		rabs, err := filepath.Abs(root)
		if err != nil {
			rabs = root
		}
		rabs = filepath.Clean(rabs)

		rel, err := filepath.Rel(rabs, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return abs, nil
		}
	}

	return "", fmt.Errorf("read_file: path %q escapes allowed policy directories", p)
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

