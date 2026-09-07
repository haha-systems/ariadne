package invocation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Event names shared by every workflow. Kept as plain strings (not a Status
// enum) because the set is open-ended and consumers only ever match on the
// exact name.
const (
	EventInvocationStarted   = "invocation_started"
	EventInvocationCompleted = "invocation_completed"
	EventInvocationFailed    = "invocation_failed"
	EventAgentSelected       = "agent_selected"
	EventEscalationRequested = "escalation_requested"
	EventEscalationApproved  = "escalation_approved"
	EventEscalationDenied    = "escalation_denied"
	EventMemoryRead          = "memory_read"
	EventMemoryWritten       = "memory_written"
	EventWorkspaceCreated    = "workspace_created"
	EventWorkspaceReleased   = "workspace_released"
	EventBudgetUpdated       = "budget_updated"
	EventMemoryWriteFailed   = "memory_write_failed"

	// Research-workflow events. These make the Ari -> QAC -> Astra cycle
	// auditable from run.jsonl alone: which agent ran under which
	// identity, what context it was given, how an escalation scored, and
	// where control went next.
	EventResearchCycleStarted     = "research_cycle_started"
	EventAriInvocationStarted     = "ari_invocation_started"
	EventAriInvocationCompleted   = "ari_invocation_completed"
	EventAriIdentity              = "ari_identity"
	EventFrontierLoaded           = "frontier_loaded"
	EventMemoryPreflightPassed    = "memory_preflight_passed"
	EventMemoryPreflightFailed    = "memory_preflight_failed"
	EventEscalationScored         = "escalation_scored"
	EventAstraInvocationStarted   = "astra_invocation_started"
	EventAstraInvocationCompleted = "astra_invocation_completed"
	EventControlReturnedToAri     = "control_returned_to_ari"

	// Preserved from the pre-v2 supervisor so existing run.jsonl consumers
	// see no schema change.
	EventRunStarted     = "run_started"
	EventRunTimeout     = "run_timeout"
	EventRunFailed      = "run_failed"
	EventRunSucceeded   = "run_succeeded"
	EventProviderStdout = "provider_stdout"
)

// EventWriter appends structured JSON event lines to a run's JSONL log and,
// when used as an io.Writer, streams provider stdout/stderr into the same
// log (one provider_stdout event per line) while echoing each line to
// stderr with a run-ID prefix. It replaces the pre-v2 supervisor's
// logEvent/providerLogWriter pair with one shared type so the issue
// workflow and the research workflow emit an identical event schema.
type EventWriter struct {
	w      *os.File
	prefix string
	buf    []byte
}

// NewEventWriter wraps w (typically a run's run.jsonl file) for a given run
// ID, used to prefix streamed stdout lines.
func NewEventWriter(w *os.File, runID string) *EventWriter {
	return &EventWriter{w: w, prefix: "[" + runID + "] "}
}

// Log writes a structured JSON event line. fields is mutated (timestamp and
// event keys are added) and must not be reused by the caller afterward.
func (ew *EventWriter) Log(event string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	fields["event"] = event
	line, _ := json.Marshal(fields)
	ew.w.Write(append(line, '\n')) //nolint:errcheck
}

// Write implements io.Writer, splitting the provider's output into lines and
// recording each as a provider_stdout event while echoing it to stderr.
func (ew *EventWriter) Write(p []byte) (int, error) {
	ew.buf = append(ew.buf, p...)

	for {
		idx := bytes.IndexByte(ew.buf, '\n')
		if idx < 0 {
			break
		}
		line := ew.buf[:idx+1]

		entry := map[string]any{
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"event":     EventProviderStdout,
			"line":      string(bytes.TrimRight(line, "\n")),
		}
		encoded, _ := json.Marshal(entry)
		ew.w.Write(append(encoded, '\n')) //nolint:errcheck

		fmt.Fprintf(os.Stderr, "%s%s", ew.prefix, line)

		ew.buf = ew.buf[idx+1:]
	}

	return len(p), nil
}
