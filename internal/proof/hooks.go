package proof

import (
	"context"
	"fmt"
	"os/exec"
)

// RunHooks executes all configured post-run hook commands, passing the
// proof summary JSON path as $1 to each hook script.
func RunHooks(ctx context.Context, hooks []string, summaryPath string) error {
	for _, hook := range hooks {
		cmd := exec.CommandContext(ctx, "sh", "-c", hook, "--", summaryPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("hook %q: %w: %s", hook, err, out)
		}
	}
	return nil
}
