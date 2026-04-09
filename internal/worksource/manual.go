package worksource

import (
	"context"

	"github.com/haha-systems/ariadne/internal/domain"
)

// ManualSource is a no-op WorkSource used for directly launched Ariadne runs.
// It satisfies the full interface without mutating external systems.
type ManualSource struct{}

func NewManualSource() *ManualSource {
	return &ManualSource{}
}

func (s *ManualSource) Name() string {
	return "manual"
}

func (s *ManualSource) Poll(_ context.Context) ([]*domain.Task, error) {
	return nil, nil
}

func (s *ManualSource) Claim(_ context.Context, _ *domain.Task) error {
	return nil
}

func (s *ManualSource) PostResult(_ context.Context, _ *domain.Task, _ string) error {
	return nil
}

func (s *ManualSource) ListOpenPRs(_ context.Context) ([]*domain.Task, error) {
	return nil, nil
}

func (s *ManualSource) RecordRebaseOutcome(_ context.Context, _ *domain.Task, _ bool, _ string) error {
	return nil
}

func (s *ManualSource) ListPRsNeedingReview(_ context.Context) ([]*domain.Task, error) {
	return nil, nil
}

func (s *ManualSource) ListPRsNeedingRevision(_ context.Context) ([]*domain.Task, error) {
	return nil, nil
}

func (s *ManualSource) RecordReviewOutcome(_ context.Context, _ *domain.Task, _ bool, _ string) error {
	return nil
}

func (s *ManualSource) MarkPRNeedsReview(_ context.Context, _ int, _ int) error {
	return nil
}
