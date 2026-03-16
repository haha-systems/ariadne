package worksource

import (
	"context"

	"github.com/haha-systems/conductor/internal/domain"
)

// WorkSource is the interface all work source implementations must satisfy.
type WorkSource interface {
	// Name returns the source identifier (e.g. "github", "linear").
	Name() string
	// Poll returns pending tasks from this source.
	Poll(ctx context.Context) ([]*domain.Task, error)
	// Claim atomically marks a task as claimed in the source system.
	// Returns an error if the task was already claimed by another process.
	Claim(ctx context.Context, task *domain.Task) error
	// PostResult posts a proof summary comment back to the originating task/issue.
	PostResult(ctx context.Context, task *domain.Task, summary string) error
}
