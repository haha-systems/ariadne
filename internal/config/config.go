package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// Config is the top-level ariadne.toml structure.
type Config struct {
	Ariadne     AriadneConfig             `toml:"ariadne"`
	WorkSources WorkSourcesConfig         `toml:"work_sources"`
	Providers   map[string]ProviderConfig `toml:"providers"`
	Skills      map[string]SkillConfig    `toml:"skills"` // merged from TOML and discovery
	Routing     RoutingConfig             `toml:"routing"`
	Proof       ProofConfig               `toml:"proof"`
	Sandbox     SandboxConfig             `toml:"sandbox"`
	Hooks       []string                  `toml:"hooks"`
	// Personas is populated by discoverPersonas; not read from TOML directly.
	Personas map[string]PersonaConfig
}

type SkillConfig struct {
	Name        string            `toml:"-"`
	Description string            `toml:"description"`
	Command     string            `toml:"command"`
	Env         map[string]string `toml:"env"`
	Dir         string            `toml:"-"` // Path to the skill directory if discovered
	IsPackage   bool              `toml:"-"` // True if discovered via SKILL.md
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
	Project     string   `toml:"project"`
	TeamID      string   `toml:"team_id"`
	StateFilter []string `toml:"state_filter"`
}

type ProviderConfig struct {
	Enabled         bool     `toml:"enabled"`
	Binary          string   `toml:"binary"`
	ExtraArgs       []string `toml:"extra_args"`
	CostPer1kTokens float64  `toml:"cost_per_1k_tokens"`
}

type RoutingConfig struct {
	Strategy      string            `toml:"strategy"`
	LabelRoutes   map[string]string `toml:"label_routes"`
	PersonaRoutes map[string]string `toml:"persona_routes"`
	RouterFile    string            `toml:"router_file"`
}

type ProofConfig struct {
	RequireCIPass bool   `toml:"require_ci_pass"`
	PRBaseBranch  string `toml:"pr_base_branch"`
	PublishMode   string `toml:"publish_mode"`
	CICommand     []string `toml:"ci_command"`
}

type SandboxConfig struct {
	WorktreeDir       string            `toml:"worktree_dir"`
	TimeoutMinutes    int               `toml:"timeout_minutes"`
	PreserveOnFailure bool              `toml:"preserve_on_failure"`
	WorkflowFile      string            `toml:"workflow_file"`
	Env               map[string]string `toml:"env"`
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

	// Discover personas and skills relative to the config file location.
	repoRoot := filepath.Dir(path)
	cfg.Personas = discoverPersonas(repoRoot)

	discoveredSkills := DiscoverSkills(repoRoot)
	if cfg.Skills == nil {
		cfg.Skills = make(map[string]SkillConfig)
	}
	for name, skill := range discoveredSkills {
		// Discovered skills take precedence or complement TOML
		cfg.Skills[name] = skill
	}

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
			PublishMode:  "required",
		},
		Sandbox: SandboxConfig{
			WorktreeDir:       ".ariadne/runs",
			TimeoutMinutes:    45,
			PreserveOnFailure: true,
			WorkflowFile:      ".ariadne/WORKFLOW.md",
			Env:               map[string]string{},
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
	switch cfg.Proof.PublishMode {
	case "", "required", "allowed", "skip":
	default:
		return fmt.Errorf("proof.publish_mode must be one of required, allowed, skip")
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

// DiscoverSkills scans <repoRoot>/.ariadne/skills/ for subdirectories containing SKILL.md.
func DiscoverSkills(repoRoot string) map[string]SkillConfig {
	skillsDir := filepath.Join(repoRoot, ".ariadne", "skills")
	skills := make(map[string]SkillConfig)

	// Scan workspace skills
	scanSkillsDir(skillsDir, skills)

	// Optionally scan user skills (e.g. ~/.ariadne/skills)
	home, _ := os.UserHomeDir()
	if home != "" {
		scanSkillsDir(filepath.Join(home, ".ariadne", "skills"), skills)
	}

	return skills
}

func scanSkillsDir(root string, skills map[string]SkillConfig) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			skillFile := filepath.Join(path, "SKILL.md")
			if _, err := os.Stat(skillFile); err == nil {
				if skill, ok := parseSkillFile(skillFile); ok {
					skill.Dir = path
					skill.IsPackage = true
					skills[skill.Name] = skill
				}
			}
		}
		return nil
	})
}

func parseSkillFile(path string) (SkillConfig, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SkillConfig{}, false
	}

	// Simple YAML frontmatter parser
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return SkillConfig{}, false
	}

	parts := bytes.SplitN(data, []byte("---\n"), 3)
	if len(parts) < 3 {
		return SkillConfig{}, false
	}

	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal(parts[1], &meta); err != nil {
		return SkillConfig{}, false
	}

	if meta.Name == "" {
		meta.Name = filepath.Base(filepath.Dir(path))
	}

	return SkillConfig{
		Name:        meta.Name,
		Description: meta.Description,
	}, true
}
