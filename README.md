# Ariadne

Ariadne is a multi-provider coding agent orchestrator. It polls a work source (GitHub Issues or Linear), dispatches tasks to one or more AI coding agents (Claude Code, Codex, Gemini CLI, OpenCode, or a custom binary), and posts proof-of-work summaries back to the originating issue.

## How it works

1. **Poll** — Ariadne watches a work source for tasks matching a label filter.
2. **Claim** — A task is atomically claimed so concurrent instances don't duplicate work.
3. **Route** — The router picks a provider (round-robin, cheapest, or race) and creates an isolated git worktree for the run.
4. **Run** — The agent binary is launched inside the worktree with the task prompt delivered via `$ARIADNE_TASK_FILE`.
5. **Collect proof** — After the run, Ariadne collects a `proof/summary.json` bundle and posts it back to the issue.

## Installation

```bash
go install github.com/haha-systems/ariadne/cmd/ariadne@latest
```

Or build from source:

```bash
git clone https://github.com/haha-systems/ariadne
cd ariadne
go build -o ariadne ./cmd/ariadne
```

Requires Go 1.25+.

## Quick start

1. Copy and edit the sample config:

```bash
cp ariadne.toml.example ariadne.toml   # or start from the snippet below
```

2. Export credentials:

```bash
export GITHUB_TOKEN=ghp_...   # for GitHub work source
# or
export LINEAR_API_KEY=lin_api_...   # for Linear work source
```

3. Start the polling loop:

```bash
ariadne run
```

## Configuration

All configuration lives in `ariadne.toml`.

```toml
[ariadne]
max_concurrent_runs   = 4        # parallel agent runs
default_provider      = "claude" # fallback when no route matches
work_interval_seconds = 30       # polling cadence

[work_sources.github]
repo         = "owner/repo"
label_filter = ["ariadne"]     # only pick up issues with this label

# [work_sources.linear]
# team_id      = "TEAM-ID"
# state_filter = ["Todo", "In Progress"]

[providers.claude]
enabled          = true
binary           = "claude"
extra_args       = ["--model", "claude-sonnet-4-6", "--dangerously-skip-permissions"]
cost_per_1k_tokens = 0.003

# [providers.codex]
# enabled = true
# binary  = "codex"

# [providers.gemini]
# enabled = true
# binary  = "gemini"
# cost_per_1k_tokens = 0.001

# [providers.opencode]
# enabled = true
# binary  = "opencode"

[routing]
strategy = "round-robin"   # round-robin | cheapest | race <N>

# Route tasks carrying specific labels to a specific provider:
# [routing.label_routes]
# big-context = "gemini"

[proof]
require_ci_pass = true
pr_base_branch  = "main"

[sandbox]
worktree_dir        = ".ariadne/runs"
timeout_minutes     = 45
preserve_on_failure = true
workflow_file       = ".ariadne/WORKFLOW.md"

# Shell commands run after every successful run (receives summary path as $1):
# hooks = ["./scripts/notify.sh"]

[skills.test]
description = "Run project tests"
command     = "go test ./..."
env         = { "GO111MODULE" = "on" }
```

## Skills and Memory

Ariadne supports harness-wide persistent memory and configurable skills, allowing agents to share knowledge and execute pre-defined workflows. These features are exposed via Ariadne's MCP server.

### Skills

Skills are modular, self-contained packages that extend Ariadne's capabilities. They follow the Hermes `SKILL.md` standard and are discovered from `.ariadne/skills/` (workspace) or `~/.ariadne/skills/` (user).

A skill directory structure looks like this:

```
.ariadne/skills/my-skill/
├── SKILL.md          # Metadata and instructions
└── scripts/
    └── run.sh        # Executable logic (invoked via ariadne_run_skill)
```

The `SKILL.md` file must contain YAML frontmatter:

```markdown
---
name: my-skill
description: Does something useful
---
Instructions for the agent go here.
```

Skills are exposed via the `ariadne_run_skill` MCP tool. If a skill package contains a `scripts/run.{sh,py,js,cjs}` file, it will be executed when the skill is run. You can also define simple command-based skills in `ariadne.toml`:

```toml
[skills.test]
description = "Run project tests"
command     = "go test ./..."
```

### Memory

Ariadne provides a harness-wide persistent memory store (located at `.ariadne/memory.json`). Agents can interact with it using the following MCP tools:

- `ariadne_remember(key, value)` — Store a piece of knowledge.
- `ariadne_recall(key)` — Retrieve a piece of knowledge.
- `ariadne_forget(key)` — Delete a piece of knowledge.

## Starlark Plugins

Ariadne supports Starlark for "Logic Plugins". Currently, you can use Starlark to define complex routing rules.

### Custom Routing

To use custom routing, set `router_file` in your `ariadne.toml`:

```toml
[routing]
strategy    = "round-robin"
router_file = ".ariadne/route.star"
```

Then create `.ariadne/route.star` with a `route(task)` function:

```python
def route(task):
    if "urgent" in task["labels"]:
        return "gemini"
    if "slow" in task["labels"]:
        return {"provider": "claude", "persona": "senior-dev"}
    if "race" in task["labels"]:
        return {"providers": ["claude", "gemini"]}
    return None # Fall back to default routing
```

### Custom Commands

You can add new subcommands to the `ariadne` CLI by placing Starlark scripts in `.ariadne/commands/`.

Example `.ariadne/commands/hello.star`:

```python
name = "hello"
description = "Say hello"

def run(args):
    print("Hello from Starlark!")
    if len(args) > 0:
        print("Args: " + ", ".join(args))
```

Then run it with: `ariadne hello world`.

## Providers

| Key        | Binary      | Notes                               |
|------------|-------------|-------------------------------------|
| `claude`   | `claude`    | Claude Code CLI; supports cost tracking |
| `codex`    | `codex`     | OpenAI Codex CLI                    |
| `gemini`   | `gemini`    | Gemini CLI; supports cost tracking  |
| `opencode` | `opencode`  | OpenCode CLI; supports cost tracking |
| _custom_   | any binary  | Any executable; set `binary` field  |

The agent binary receives the task prompt via the `CONDUCTOR_TASK_FILE` environment variable (path to a markdown file containing the full task prompt).

## Routing strategies

| Strategy      | Behaviour                                                        |
|---------------|------------------------------------------------------------------|
| `round-robin` | Cycles through enabled providers in order (default)              |
| `cheapest`    | Picks the provider with the lowest estimated cost for the prompt |
| `race <N>`    | Launches N runs in parallel; first success wins                  |

### Per-task overrides

Add a `ariadne:` YAML front-matter block to an issue description to override routing for that specific task:

```yaml
---
ariadne:
  agent: gemini          # pin to a specific provider
  routing: cheapest      # or: race 2
  timeout_minutes: 60
  env:
    MY_VAR: value
---

Task description goes here.
```

## CLI reference

```
ariadne [--config ariadne.toml] <command>
```

| Command                         | Description                                          |
|---------------------------------|------------------------------------------------------|
| `run`                           | Start polling and dispatching agents                 |
| `collect-proof --run-id <id>`   | Print the proof bundle for a completed run           |
| `land --run-id <id>`            | Rebase, re-run CI, and merge a reviewed run          |
| `cost`                          | Show USD cost summary for all completed runs         |

## Work sources

### GitHub Issues

```toml
[work_sources.github]
repo         = "owner/repo"
label_filter = ["ariadne"]
```

Requires `GITHUB_TOKEN` with `repo` scope. Ariadne polls for open issues carrying all listed labels, claims each by adding a `ariadne:claimed` label, and posts proof as a comment.

### Linear

```toml
[work_sources.linear]
team_id      = "ENG"
state_filter = ["Todo"]
```

Requires `LINEAR_API_KEY`. Ariadne polls for issues in the given team whose state matches the filter, claims them by moving to a "claimed" state, and posts proof as a comment.

## Proof and landing

After a successful run, Ariadne writes a `proof/summary.json` file inside the worktree:

```json
{
  "run_id":   "run_1234567890",
  "task_id":  "42",
  "provider": "claude",
  "cost_usd": 0.0312
}
```

Use `ariadne land --run-id <id>` to rebase the worktree branch on `main`, wait for CI to pass, and merge automatically.

## `.ariadne/` directory structure

Ariadne looks for several files inside a `.ariadne/` directory at the root of your repo. None are required, but they let you customise how agents behave.

| Path | Purpose |
|------|---------|
| `.ariadne/WORKFLOW.md` | Injected at the top of every task prompt — use for global agent instructions |
| `.ariadne/REBASE_WORKFLOW.md` | Injected into rebase task prompts (optional, falls back to none) |
| `.ariadne/personas/<name>/` | Persona directory — see Personas section |
| `.ariadne/personas/<name>/SOUL.md` | Core identity injected as the Role section of the task prompt |
| `.ariadne/personas/<name>/PERSONALITY.md` | Behavioral traits appended to the Role section |
| `.ariadne/personas/<name>/CLAUDE.md` | Copied to the worktree root before the agent runs |
| `.ariadne/personas/<name>/AGENTS.md` | Replaces `WORKFLOW.md` for this persona |
| `.ariadne/personas/<name>/persona.toml` | Optional: `provider = "claude"` to override the default provider |
| `.ariadne/runs/` | Auto-managed run worktrees (configured via `worktree_dir`) |

The `workflow_file` and `worktree_dir` config keys control which paths Ariadne uses:

```toml
[sandbox]
worktree_dir  = ".ariadne/runs"
workflow_file = ".ariadne/WORKFLOW.md"
```

## License

Apache 2.0
