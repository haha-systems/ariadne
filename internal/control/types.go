// Package control is the narrow boundary between the research runtime and
// any interface that observes or steers it — today the TUI, tomorrow an
// HTTP API or a remote client.
//
// Everything here is a projection or a command. None of it is state: the
// research runtime, QAC scheduler, and memory.Store remain the source of
// truth, and Ariadne stays fully usable through the CLI with no interface
// attached. A UI that reached past this package into internal/research or
// internal/scheduler would end up maintaining a second, divergent copy of
// the research state, which is exactly what this boundary exists to
// prevent.
//
// The package deliberately depends on nothing else in the tree, so a UI can
// import it without pulling in providers, schedulers, or memory backends.
package control

import "time"

// RunState is the research loop's lifecycle state.
type RunState string

const (
	RunStateIdle RunState = "idle"
	// RunStateRunning means cycles are executing.
	RunStateRunning RunState = "running"
	// RunStatePauseRequested means a pause was accepted but the runtime has
	// not yet reached a safe boundary. It is a distinct state because the
	// gap between asking and stopping is exactly what a user needs to see:
	// a pause never kills a running model process.
	RunStatePauseRequested RunState = "pause_requested"
	RunStatePaused         RunState = "paused"
	RunStateStopped        RunState = "stopped"
	RunStateFailed         RunState = "failed"
)

// Display renders a RunState for a status header, in caps so it reads as a
// state even without colour (see the monochrome requirement).
func (s RunState) Display() string {
	switch s {
	case RunStatePauseRequested:
		return "PAUSE REQUESTED"
	case "":
		return "IDLE"
	default:
		out := make([]rune, 0, len(s))
		for _, r := range s {
			if r == '_' {
				out = append(out, ' ')
				continue
			}
			if r >= 'a' && r <= 'z' {
				r -= 'a' - 'A'
			}
			out = append(out, r)
		}
		return string(out)
	}
}

// AgentState is one agent's current activity.
type AgentState string

const (
	AgentIdle    AgentState = "idle"
	AgentWorking AgentState = "working"
	// AgentWaiting means the agent is blocked on something outside itself —
	// most often a manual QAC approval.
	AgentWaiting AgentState = "waiting"
	AgentBlocked AgentState = "blocked"
	AgentFailed  AgentState = "failed"
)

// AgentSnapshot is one agent's presentation state.
type AgentSnapshot struct {
	Name     string
	Model    string
	Provider string
	Role     string

	State AgentState

	CurrentTask string
	StartedAt   time.Time

	LastSummary string

	CostClass     string
	ScarcityClass string
}

// Elapsed is how long the current invocation has been running, or zero when
// the agent is not working.
func (a AgentSnapshot) Elapsed() time.Duration {
	if a.StartedAt.IsZero() || a.State != AgentWorking {
		return 0
	}
	return time.Since(a.StartedAt)
}

// EscalationSummary is one QAC escalation, request through decision. It
// carries the scoring factors as well as the outcome, because an escalation
// record that shows only "denied" is not inspectable — the whole point of
// QAC is being able to see why scarce cognition was or wasn't spent.
type EscalationSummary struct {
	ID              string
	RequestedAt     time.Time
	RequestingAgent string
	TargetAgent     string

	Question string
	Reason   string

	Score                float64
	Uncertainty          float64
	Importance           float64
	ExpectedGain         float64
	FailedLocalAttempts  int
	CheaperTestAvailable bool
	Duplicate            bool

	ScarcityClass string

	// Decision is "pending", "approved", or "denied".
	Decision       string
	DecisionReason string
	DecidedAt      time.Time

	Budget QACSnapshot
}

// Escalation decision values.
const (
	DecisionPending  = "pending"
	DecisionApproved = "approved"
	DecisionDenied   = "denied"
)

// QACSnapshot is the scheduler's quota state.
//
// CallsRemainingKnown distinguishes "no calls left" from "no limit
// configured": an unlimited budget is a real configuration, and rendering
// it as 0 remaining would be actively misleading.
type QACSnapshot struct {
	Mode string

	CallsUsed           int
	CallsRemaining      int
	CallsRemainingKnown bool

	CooldownUntil *time.Time

	PendingEscalation *EscalationSummary
}

// Steering lifecycle states.
const (
	SteeringPending    = "pending"
	SteeringApplied    = "applied"
	SteeringSuperseded = "superseded"
	SteeringCancelled  = "cancelled"
)

// SteeringRecord is one user instruction to the research programme.
// Steering is part of the research record, not chat: it is persisted,
// carries a lifecycle, and is auditable after the fact.
type SteeringRecord struct {
	ID      string
	Project string

	ReceivedAt time.Time
	Message    string
	Source     string
	Status     string

	CycleReceived int
	// CycleApplied is -1 until the instruction reaches an agent prompt.
	CycleApplied int
}

// ResearchSnapshot is the whole presentation projection: everything a
// status view needs in one consistent read. It is a projection, never the
// primary store — discarding it and rebuilding from the runtime must always
// be safe.
type ResearchSnapshot struct {
	Project string
	State   RunState
	Cycle   int

	ActiveAgent string

	Agents []AgentSnapshot

	Frontier          string
	ActiveHypotheses  int
	RejectedCount     int
	OpenQuestionCount int
	ConfoundCount     int

	CurrentExperiment string
	LatestResult      string

	QAC QACSnapshot

	PendingSteering int

	// MemoryBackend names the configured memory.Store implementation, shown
	// as metadata only — no interface should branch on it.
	MemoryBackend string

	// LastError is the most recent recoverable runtime error, surfaced so a
	// UI can show it in an alert area without the runtime having to crash.
	LastError string

	TakenAt time.Time
}

// Event is one structured runtime event, decoupled from the JSONL writer's
// encoding so a subscriber never has to parse log lines.
type Event struct {
	Name   string
	Time   time.Time
	Fields map[string]any
}

// String returns a field as a string, or "" when absent or another type.
func (e Event) String(key string) string {
	if e.Fields == nil {
		return ""
	}
	s, _ := e.Fields[key].(string)
	return s
}

// Number returns a numeric field, and whether it was present and numeric.
// JSON round-trips make every number a float64, so both shapes are handled.
func (e Event) Number(key string) (float64, bool) {
	if e.Fields == nil {
		return 0, false
	}
	switch v := e.Fields[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	default:
		return 0, false
	}
}

// Log stream names.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
	// StreamSystem carries the runtime's own narration of an agent's
	// lifecycle, so a log view still reads coherently for a provider that
	// produces no output at all.
	StreamSystem = "system"
)

// AgentLogEntry is one line of an agent's output, tagged so Ari's and
// Astra's streams stay separable — merging them produces an unreadable
// interleaving precisely when a user most needs to follow one agent.
type AgentLogEntry struct {
	Project    string
	Agent      string
	Invocation string
	Time       time.Time
	Stream     string
	Text       string
}

// MemoryRecord is one memory.Store record, projected for display.
type MemoryRecord struct {
	ID           string
	Kind         string
	Subject      string
	Summary      string
	AgentID      string
	State        string
	Confidence   float64
	EvidenceRefs []string
	CreatedAt    time.Time
	// Reason is the accept/reject rationale where the backend records one.
	Reason string
}

// MemoryView is the structured read of research memory a UI renders. It is
// built from the configured memory.Store through the controller, so no
// interface ever needs Ghostdive-specific behavior; Backend is metadata.
type MemoryView struct {
	Backend string

	Frontier           MemoryRecord
	Observations       []MemoryRecord
	ActiveHypotheses   []MemoryRecord
	RejectedHypotheses []MemoryRecord
	OpenQuestions      []MemoryRecord
	Confounds          []MemoryRecord
	Decisions          []MemoryRecord
	LatestAstraResult  MemoryRecord
}
