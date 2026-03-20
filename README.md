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
```

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
