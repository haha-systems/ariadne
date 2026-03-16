package proof

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// Lander performs the safe-land operation: rebase → CI → merge.
type Lander struct {
	cfg Config
}

func NewLander(cfg Config) *Lander {
	return &Lander{cfg: cfg}
}

// Land rebases the run's worktree onto the latest base branch, re-runs CI,
// and fast-forward pushes to origin only if CI passes.
// Returns the HEAD SHA of the landed commit.
func (l *Lander) Land(ctx context.Context, worktreePath string) (string, error) {
	// 1. Fetch latest base branch.
	if err := l.git(ctx, worktreePath, "fetch", "origin", l.cfg.PRBaseBranch); err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}

	// 2. Rebase onto origin/<base>.
	if err := l.git(ctx, worktreePath, "rebase", "origin/"+l.cfg.PRBaseBranch); err != nil {
		l.git(ctx, worktreePath, "rebase", "--abort") //nolint:errcheck
		return "", fmt.Errorf("rebase onto %s: %w", l.cfg.PRBaseBranch, err)
	}

	// 3. Re-run CI in the rebased worktree.
	collector := New(l.cfg)
	ci, ciErr := collector.runCI(ctx, worktreePath)
	if ciErr != nil || !ci.Passed {
		return "", fmt.Errorf("CI failed after rebase (passed=%v, failures=%d): %v",
			ci.Passed, ci.Failures, ciErr)
	}

	// 4. Push HEAD to origin/<base> via fast-forward.
	// We never force-push; if the push is rejected the operator must resolve manually.
	if err := l.git(ctx, worktreePath, "push", "origin",
		fmt.Sprintf("HEAD:%s", l.cfg.PRBaseBranch)); err != nil {
		return "", fmt.Errorf("push to %s: %w", l.cfg.PRBaseBranch, err)
	}

	// 5. Return the landed commit SHA for logging.
	out, err := l.gitOutput(ctx, worktreePath, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse: %w", err)
	}
	return string(bytes.TrimSpace(out)), nil
}

func (l *Lander) git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return nil
}

func (l *Lander) gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %v: %w", args, err)
	}
	return out, nil
}

