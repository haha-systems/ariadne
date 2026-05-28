package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// gatewayProofSummaryFile is the filename (inside the run worktree's "proof/"
// directory) written by ProofSummaryResultHandler. Chosen to be distinct from
// the legacy collector's "summary.json" (which contains a rich domain.ProofBundle)
// so consumers can reliably distinguish the minimal gateway format.
const gatewayProofSummaryFile = "gateway_summary.json"

// LoggingResultHandler is the default built-in ResultHandler.
// It emits a structured log line at Info level on every terminal run.
// It is automatically registered by gateway.New unless the caller
// explicitly supplies a noopResultHandler in Config.ResultHandlers.
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
	dur := 0.0
	if run.FinishedAt != nil {
		dur = run.FinishedAt.Sub(run.StartedAt).Seconds()
	}
	h.logger.Info("gateway run completed",
		"run_id", run.ID,
		"title", run.Title,
		"provider", run.Provider,
		"persona", run.Persona,
		"status", run.Status,
		"worktree", run.Worktree,
		"duration_seconds", dur,
		"source", inv.Source,
		"last_error", run.LastError,
	)
	return nil
}

// noopResultHandler is used internally when the user explicitly wants no handlers
// (including suppressing the automatic logging handler).
type noopResultHandler struct{}

func (noopResultHandler) Handle(ctx context.Context, run *Run, inv *Invocation, outcome any) error {
	return nil
}

// =============================================================================
// Built-in ResultHandlers for common post-run delivery needs.
// These demonstrate the intended extension model for adapters.
// =============================================================================

// ProofSummaryResultHandler is a built-in handler that writes a minimal
// gateway_summary.json file (see gatewayProofSummaryFile) into the run's
// worktree "proof/" directory (creating the directory if necessary).
//
// This provides a lightweight, always-available proof artifact for direct
// gateway runs and MCP usage, without the full CI/PR machinery of the legacy
// operator's proof.Collector (which writes the richer proof/summary.json for
// worksource-driven runs using the collector).
//
// The written file is intentionally simple and stable for scripts/adapters
// to consume. It is best-effort: missing worktree or write failures are
// surfaced as errors from Handle (gateway ignores them for the run itself).
type ProofSummaryResultHandler struct{}

// NewProofSummaryResultHandler returns a handler that writes proof/gateway_summary.json.
func NewProofSummaryResultHandler() *ProofSummaryResultHandler {
	return &ProofSummaryResultHandler{}
}

// Handle implements ResultHandler. It is safe for concurrent use.
func (h *ProofSummaryResultHandler) Handle(ctx context.Context, run *Run, inv *Invocation, outcome any) error {
	if run.Worktree == "" {
		return nil // direct runs without worktree have nothing to summarize
	}

	proofDir := filepath.Join(run.Worktree, "proof")
	if err := os.MkdirAll(proofDir, 0o755); err != nil {
		return fmt.Errorf("proof summary: mkdir %s: %w", proofDir, err)
	}

	dur := 0.0
	if run.FinishedAt != nil {
		dur = run.FinishedAt.Sub(run.StartedAt).Seconds()
	}
	finished := ""
	if run.FinishedAt != nil {
		finished = run.FinishedAt.UTC().Format(time.RFC3339)
	}

	summary := map[string]any{
		"run_id":           run.ID,
		"title":            run.Title,
		"provider":         run.Provider,
		"persona":          run.Persona,
		"status":           string(run.Status),
		"worktree":         run.Worktree,
		"started_at":       run.StartedAt.UTC().Format(time.RFC3339),
		"finished_at":      finished,
		"duration_seconds": dur,
		"error":            run.LastError,
		"source":           inv.Source,
		"source_url":       inv.SourceURL,
		"last_event":       run.LastEvent,
	}

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("proof summary: marshal: %w", err)
	}

	path := filepath.Join(proofDir, gatewayProofSummaryFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("proof summary: write %s: %w", path, err)
	}
	// Populate ProofPath on the snapshot so that the gateway can reflect it on
	// the live Run (handlers receive copies; gateway propagates fields like this
	// after the handler loop per the Run field contract).
	run.ProofPath = path
	return nil
}

// WebhookResultHandler is a built-in handler that performs a best-effort
// HTTP POST of a JSON run summary to a caller-supplied URL on completion.
//
// Configuration (URL, timeout, extra headers) is performed exclusively via
// the constructor and functional options (see NewWebhookResultHandler and
// With* helpers). This keeps gateway.Config decoupled from webhook specifics;
// future TOML support can construct the handler in main/adapter code and
// pass it via Config.ResultHandlers or RegisterResultHandler.
//
// The handler is intentionally minimal:
//   - Uses stdlib net/http only.
//   - No retries (add in Phase 3 if needed).
//   - Errors are returned from Handle (gateway treats all handlers as
//     best-effort and ignores returned errors).
//   - Safe for concurrent calls (each Handle creates its own request).
type WebhookResultHandler struct {
	url     string
	client  *http.Client
	headers map[string]string
}

// webhookOptions holds constructor configuration.
type webhookOptions struct {
	timeout time.Duration
	headers map[string]string
}

// WebhookOption configures a WebhookResultHandler at construction time.
type WebhookOption func(*webhookOptions)

// WithWebhookTimeout sets the client timeout for the POST (default 10s).
func WithWebhookTimeout(d time.Duration) WebhookOption {
	return func(o *webhookOptions) {
		if d > 0 {
			o.timeout = d
		}
	}
}

// WithWebhookHeader adds (or overrides) an HTTP header for the webhook POST.
func WithWebhookHeader(key, value string) WebhookOption {
	return func(o *webhookOptions) {
		if o.headers == nil {
			o.headers = make(map[string]string)
		}
		o.headers[key] = value
	}
}

// NewWebhookResultHandler creates a handler that will POST a JSON payload
// containing run + invocation summary information to url.
//
// Example for an adapter (e.g. cron or future Discord bridge):
//
//	h := gateway.NewWebhookResultHandler(
//	    "https://example.com/ariadne/callback",
//	    gateway.WithWebhookTimeout(15*time.Second),
//	    gateway.WithWebhookHeader("X-Ariadne-Token", secret),
//	)
//	gw.RegisterResultHandler(h)
func NewWebhookResultHandler(url string, opts ...WebhookOption) *WebhookResultHandler {
	wo := webhookOptions{
		timeout: 10 * time.Second,
		headers: map[string]string{},
	}
	for _, opt := range opts {
		opt(&wo)
	}
	return &WebhookResultHandler{
		url:     url,
		client:  &http.Client{Timeout: wo.timeout},
		headers: wo.headers,
	}
}

// Handle implements ResultHandler. It builds a compact JSON body and POSTs it.
// The ctx passed by the gateway is used for the request.
func (h *WebhookResultHandler) Handle(ctx context.Context, run *Run, inv *Invocation, outcome any) error {
	if h.url == "" {
		return nil
	}

	dur := 0.0
	if run.FinishedAt != nil {
		dur = run.FinishedAt.Sub(run.StartedAt).Seconds()
	}
	finished := ""
	if run.FinishedAt != nil {
		finished = run.FinishedAt.UTC().Format(time.RFC3339)
	}

	payload := map[string]any{
		"event": "run.completed",
		"run": map[string]any{
			"id":               run.ID,
			"title":            run.Title,
			"status":           string(run.Status),
			"provider":         run.Provider,
			"persona":          run.Persona,
			"worktree":         run.Worktree,
			"started_at":       run.StartedAt.UTC().Format(time.RFC3339),
			"finished_at":      finished,
			"duration_seconds": dur,
			"error":            run.LastError,
			"proof_path":       run.ProofPath, // populated when ProofSummaryResultHandler (or other) runs successfully
		},
		"invocation": map[string]any{
			"source":     inv.Source,
			"source_url": inv.SourceURL,
			"prompt":     inv.Prompt,
			"metadata":   inv.Metadata,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhook marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook request %s: %w", h.url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook post %s: %w", h.url, err)
	}
	defer resp.Body.Close()

	// Drain body (ignore content for minimal impl).
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook %s: server returned status %d", h.url, resp.StatusCode)
	}
	return nil
}

// WithNoDefaultHandlers is a sentinel that can be used in future Config options
// if we want to disable the automatic logging handler.
