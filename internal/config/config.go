package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the top-level ariadne.toml structure.
type Config struct {
	Ariadne       AriadneConfig                 `toml:"ariadne"`
	WorkSources   WorkSourcesConfig             `toml:"work_sources"`
	Providers     map[string]ProviderConfig     `toml:"providers"`
	AgentProfiles map[string]AgentProfileConfig `toml:"agent_profiles"`
	Routing       RoutingConfig                 `toml:"routing"`
	Proof         ProofConfig                   `toml:"proof"`
	Sandbox       SandboxConfig                 `toml:"sandbox"`
	Memory        MemoryConfig                  `toml:"memory"`
	QAC           QACConfig                     `toml:"qac"`
	Research      ResearchConfig                `toml:"research"`
	Hooks         []string                      `toml:"hooks"`
	// Personas is populated by discoverPersonas; not read from TOML directly.
	Personas map[string]PersonaConfig
}

type AriadneConfig struct {
	MaxConcurrentRuns   int    `toml:"max_concurrent_runs"`
	DefaultProvider     string `toml:"default_provider"`
	WorkIntervalSeconds int    `toml:"work_interval_seconds"`
}

type WorkSourcesConfig struct {
	GitHub *GitHubSourceConfig `toml:"github"`
	Linear *LinearSourceConfig `toml:"linear"`
}

type GitHubSourceConfig struct {
	Repo           string   `toml:"repo"`
	LabelFilter    []string `toml:"label_filter"`
	AllowedAuthors []string `toml:"allowed_authors"` // empty means allow all
}

type LinearSourceConfig struct {
	TeamID      string   `toml:"team_id"`
	StateFilter []string `toml:"state_filter"`
}

type ProviderConfig struct {
	Enabled         bool     `toml:"enabled"`
	Binary          string   `toml:"binary"`
	ExtraArgs       []string `toml:"extra_args"`
	CostPer1kTokens float64  `toml:"cost_per_1k_tokens"`
}

// AgentProfileConfig describes one entry under [agent_profiles.<name>]. It is
// orthogonal to Providers/Personas: Provider names which provider adapter
// executes the profile, Model is passed as a per-invocation override (only
// honored by providers that support it), and CostClass/ScarcityClass drive
// scheduler decisions without exposing the model name to scheduling logic.
type AgentProfileConfig struct {
	Provider      string `toml:"provider"`
	Model         string `toml:"model"`
	Persona       string `toml:"persona"`
	CostClass     string `toml:"cost_class"`
	ScarcityClass string `toml:"scarcity_class"`
}

type RoutingConfig struct {
	Strategy      string            `toml:"strategy"`
	LabelRoutes   map[string]string `toml:"label_routes"`
	PersonaRoutes map[string]string `toml:"persona_routes"`
}

type ProofConfig struct {
	RequireCIPass bool   `toml:"require_ci_pass"`
	PRBaseBranch  string `toml:"pr_base_branch"`
}

type SandboxConfig struct {
	WorktreeDir       string `toml:"worktree_dir"`
	TimeoutMinutes    int    `toml:"timeout_minutes"`
	PreserveOnFailure bool   `toml:"preserve_on_failure"`
	WorkflowFile      string `toml:"workflow_file"`
}

// MemoryConfig selects the memory.Store backend. Ghostdive is optional —
// when nil, the runtime uses the in-memory backend (package
// internal/memory/inmemory) with no external dependency.
type MemoryConfig struct {
	Ghostdive *GhostdiveConfig `toml:"ghostdive"`
}

// GhostdiveConfig points at a Ghostdive instance. The API key is read from
// the GHOSTDIVE_API_KEY environment variable, matching the GITHUB_TOKEN /
// LINEAR_API_KEY convention used by the work sources.
type GhostdiveConfig struct {
	BaseURL          string            `toml:"base_url"`
	SpaceIDByProject map[string]string `toml:"space_id_by_project"`
}

// QACConfig configures Quota-Aware Cognition escalation policy. It covers
// a single scarce agent (Astra) for the MVP, per spec §47 ("the MVP only
// needs Ari -> Astra").
type QACConfig struct {
	// Approval is the ControlMode: "automatic", "manual", or "disabled".
	Approval           string `toml:"approval"`
	MaxCallsPerCycle   int    `toml:"max_calls_per_cycle"`
	MaxCallsPerSession int    `toml:"max_calls_per_session"`
	CooldownMinutes    int    `toml:"cooldown_minutes"`
}

// ResearchConfig declares research projects the `ariadne research` commands
// can operate on.
type ResearchConfig struct {
	Projects map[string]ResearchProjectConfig `toml:"projects"`
}

// ResearchProjectConfig names the two agent profiles (see AgentProfileConfig)
// a research project uses: a continuously-running routine agent and a
// scarce frontier agent invoked only on approved escalation.
type ResearchProjectConfig struct {
	Repo          string `toml:"repo"`
	RoutineAgent  string `toml:"routine_agent"`
	FrontierAgent string `toml:"frontier_agent"`
	// GuidanceFile is durable, project-specific instruction text (the
	// research team's overall mandate) prepended to every Ari/Astra
	// prompt. Defaults to .ariadne/research/<project>/AGENTS.md if unset;
	// a missing file is not an error (guidance is optional).
	GuidanceFile string `toml:"guidance_file"`
}

// PersonaConfig describes a named agent persona discovered from .ariadne/personas/<name>/.
type PersonaConfig struct {
	Name        string // populated from directory name
	Provider    string `toml:"provider"` // from persona.toml, optional
	Dir         string // absolute path to persona directory
	DisplayName string // from persona.toml "name" — used as git author name
	Email       string // from persona.toml "email" — used as git author email
}

// personaTOML holds the optional persona.toml fields.
type personaTOML struct {
	Provider string `toml:"provider"`
	Name     string `toml:"name"`
	Email    string `toml:"email"`
}

// Load reads and parses a ariadne.toml file, applying defaults.
// repoRoot is the directory containing ariadne.toml (used to discover personas).
func Load(path string) (*Config, error) {
	cfg := defaults()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Discover personas relative to the config file location.
	repoRoot := filepath.Dir(path)
	cfg.Personas = discoverPersonas(repoRoot)

	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Ariadne: AriadneConfig{
			MaxConcurrentRuns:   4,
			DefaultProvider:     "claude",
			WorkIntervalSeconds: 30,
		},
		Routing: RoutingConfig{
			Strategy:      "round-robin",
			LabelRoutes:   map[string]string{},
			PersonaRoutes: map[string]string{},
		},
		Proof: ProofConfig{
			PRBaseBranch: "main",
		},
		Sandbox: SandboxConfig{
			WorktreeDir:       ".ariadne/runs",
			TimeoutMinutes:    45,
			PreserveOnFailure: true,
			WorkflowFile:      ".ariadne/WORKFLOW.md",
		},
		Providers: map[string]ProviderConfig{},
		Personas:  map[string]PersonaConfig{},
	}
}

func validate(cfg *Config) error {
	if cfg.Ariadne.MaxConcurrentRuns <= 0 {
		return fmt.Errorf("max_concurrent_runs must be > 0")
	}
	if cfg.Ariadne.DefaultProvider == "" {
		return fmt.Errorf("default_provider must be set")
	}
	if cfg.Sandbox.TimeoutMinutes <= 0 {
		return fmt.Errorf("sandbox.timeout_minutes must be > 0")
	}
	for name, p := range cfg.Providers {
		if p.Enabled && p.Binary == "" {
			return fmt.Errorf("provider %q: binary must be set when enabled", name)
		}
	}
	return nil
}

// discoverPersonas scans <repoRoot>/.ariadne/personas/ for subdirectories.
// Returns an empty map if the directory doesn't exist.
func discoverPersonas(repoRoot string) map[string]PersonaConfig {
	personasDir := filepath.Join(repoRoot, ".ariadne", "personas")
	entries, err := os.ReadDir(personasDir)
	if err != nil {
		return map[string]PersonaConfig{} // directory absent — not an error
	}

	personas := make(map[string]PersonaConfig)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dir := filepath.Join(personasDir, name)
		p := PersonaConfig{Name: name, Dir: dir}

		// Read optional persona.toml.
		tomlPath := filepath.Join(dir, "persona.toml")
		if data, err := os.ReadFile(tomlPath); err == nil {
			var pt personaTOML
			if _, err := toml.Decode(string(data), &pt); err == nil {
				p.Provider = pt.Provider
				p.DisplayName = pt.Name
				p.Email = pt.Email
			}
		}

		personas[name] = p
	}
	return personas
}
