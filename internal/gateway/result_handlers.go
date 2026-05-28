package gateway

import (
	"context"
	"log/slog"
)

// LoggingResultHandler is a simple built-in ResultHandler that logs run completion
// at Info level. It is registered by default when creating a Gateway via New().
type LoggingResultHandler struct {
	logger *slog.Logger
}

// NewLoggingResultHandler creates a result handler that logs to the provided
// logger. If logger is nil, it uses the default slog logger.
func NewLoggingResultHandler(logger *slog.Logger) *LoggingResultHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &LoggingResultHandler{logger: logger}
}

// Handle implements ResultHandler.
func (h *LoggingResultHandler) Handle(ctx context.Context, run *Run, inv *Invocation, outcome any) error {
	h.logger.Info("gateway run completed",
		"run_id", run.ID,
		"title", run.Title,
		"provider", run.Provider,
		"persona", run.Persona,
		"status", run.Status,
		"worktree", run.Worktree,
		"duration_seconds", run.FinishedAt.Sub(run.StartedAt).Seconds(),
		"source", inv.Source,
	)
	return nil
}

// noopResultHandler is used internally when the user explicitly wants no handlers.
type noopResultHandler struct{}

func (noopResultHandler) Handle(ctx context.Context, run *Run, inv *Invocation, outcome any) error {
	return nil
}

// WithNoDefaultHandlers is a sentinel that can be used in future Config options
// if we want to disable the automatic logging handler.
