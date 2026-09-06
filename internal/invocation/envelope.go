// Package invocation defines the generic "run one agent, get a structured
// result" primitive shared by every workflow (issue-driven and research).
package invocation

// Status is the outcome an agent reports for a single invocation.
type Status string

const (
	// StatusContinue means the agent has more routine work to do.
	StatusContinue Status = "continue"
	// StatusEscalate means the agent wants a scarcer, more capable agent
	// to weigh in before continuing.
	StatusEscalate Status = "escalate"
	// StatusComplete means the agent's goal for this invocation is done.
	StatusComplete Status = "complete"
	// StatusBlocked means the agent cannot proceed without external input.
	StatusBlocked Status = "blocked"
)

// Envelope is the structured result an agent may write back after a run.
// Agents that don't write one (e.g. today's issue-workflow providers) are
// treated as StatusComplete for backward compatibility.
type Envelope struct {
	Status           Status         `json:"status"`
	Summary          string         `json:"summary"`
	Data             map[string]any `json:"data,omitempty"`
	EscalationReason string         `json:"escalation_reason,omitempty"`
}
