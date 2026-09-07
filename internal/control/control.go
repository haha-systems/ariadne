package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ResearchCommand is one instruction from an interface to the research
// runtime. The set is deliberately small and explicit: a UI can only ask
// for things the runtime knows how to do safely, and every command is a
// value that can be logged, tested, and replayed.
type ResearchCommand interface {
	// commandName identifies the command in events and errors. It is
	// unexported so the command set stays closed to this package — an
	// interface cannot invent a new command the runtime has never seen.
	commandName() string
	// validate rejects a malformed command before it reaches the runtime.
	validate() error
}

// SteerCommand injects a user instruction into the research programme. It
// is not chat: the message becomes a persisted, auditable steering record
// that the next agent turn receives.
type SteerCommand struct {
	Message string
	// Source records who sent it, for the research record. Defaults to
	// "user" when empty.
	Source string
}

func (SteerCommand) commandName() string { return "steer" }

func (c SteerCommand) validate() error {
	if strings.TrimSpace(c.Message) == "" {
		return fmt.Errorf("%w: steering message is empty", ErrInvalidCommand)
	}
	return nil
}

// PauseCommand stops the research loop at the next safe boundary. It never
// kills a running model process: an invocation already in flight has cost
// real quota, and discarding its result to stop a few seconds sooner is a
// bad trade.
type PauseCommand struct{}

func (PauseCommand) commandName() string { return "pause" }
func (PauseCommand) validate() error     { return nil }

// ResumeCommand restarts the loop from persisted state. It must not discard
// the frontier, pending steering, QAC budget, or event history.
type ResumeCommand struct{}

func (ResumeCommand) commandName() string { return "resume" }
func (ResumeCommand) validate() error     { return nil }

// ApproveEscalationCommand approves a pending manual-mode escalation.
//
// It approves an existing request; it never invokes a scarce agent. The
// scheduler and research loop remain solely responsible for execution, so
// an interface cannot spend Astra's quota by any path the CLI lacks.
type ApproveEscalationCommand struct {
	EscalationID string
}

func (ApproveEscalationCommand) commandName() string { return "approve_escalation" }

func (c ApproveEscalationCommand) validate() error {
	if strings.TrimSpace(c.EscalationID) == "" {
		return fmt.Errorf("%w: escalation id is empty", ErrInvalidCommand)
	}
	return nil
}

// DenyEscalationCommand denies a pending manual-mode escalation. The reason
// is optional but reaches the agent as denial feedback when supplied.
type DenyEscalationCommand struct {
	EscalationID string
	Reason       string
}

func (DenyEscalationCommand) commandName() string { return "deny_escalation" }

func (c DenyEscalationCommand) validate() error {
	if strings.TrimSpace(c.EscalationID) == "" {
		return fmt.Errorf("%w: escalation id is empty", ErrInvalidCommand)
	}
	return nil
}

// CommandName exposes a command's name to callers outside this package,
// for logging and for reporting which command failed.
func CommandName(cmd ResearchCommand) string {
	if cmd == nil {
		return "<nil>"
	}
	return cmd.commandName()
}

// Validate checks a command outside the runtime, so an interface can reject
// obviously malformed input without a round-trip.
func Validate(cmd ResearchCommand) error {
	if cmd == nil {
		return fmt.Errorf("%w: nil command", ErrInvalidCommand)
	}
	return cmd.validate()
}

// Errors a controller may return. They are sentinel values so an interface
// can distinguish "you asked for something impossible right now" from "the
// runtime broke", and render the two differently.
var (
	// ErrInvalidCommand means the command was malformed.
	ErrInvalidCommand = errors.New("control: invalid command")
	// ErrNotSupported means the runtime cannot service this command in its
	// current configuration (e.g. approving under automatic QAC).
	ErrNotSupported = errors.New("control: command not supported")
	// ErrNoSuchEscalation means the referenced escalation isn't pending.
	ErrNoSuchEscalation = errors.New("control: no such pending escalation")
	// ErrNotRunning means the command needs a running loop.
	ErrNotRunning = errors.New("control: research loop is not running")
)

// ResearchController is the only surface an interface may use. It is
// intentionally read-mostly plus one Send: everything an interface can do
// to the research programme goes through a ResearchCommand, which keeps the
// set of possible UI-initiated effects finite and reviewable.
type ResearchController interface {
	// Snapshot returns a consistent projection of current research state.
	Snapshot(ctx context.Context) (ResearchSnapshot, error)

	// Subscribe streams structured runtime events. The returned channel is
	// closed when ctx is cancelled or the runtime shuts down. A subscriber
	// that stops reading must never block the research loop — implementations
	// drop events for a slow subscriber rather than applying backpressure to
	// research.
	Subscribe(ctx context.Context) (<-chan Event, error)

	// SubscribeLogs streams agent output under the same non-blocking
	// contract as Subscribe.
	SubscribeLogs(ctx context.Context) (<-chan AgentLogEntry, error)

	// RecentEvents returns up to limit recent persisted events, so an
	// interface opening mid-run has immediate context instead of an empty
	// screen until the next event happens.
	RecentEvents(ctx context.Context, limit int) ([]Event, error)

	// AgentLog returns recent buffered output for one agent, or for every
	// agent when agent is "".
	AgentLog(ctx context.Context, agent string, limit int) ([]AgentLogEntry, error)

	// Escalations returns recent QAC escalation records, newest last.
	Escalations(ctx context.Context, limit int) ([]EscalationSummary, error)

	// Memory returns the structured view of research memory.
	Memory(ctx context.Context) (MemoryView, error)

	// Steering returns the project's steering records, newest last.
	Steering(ctx context.Context) ([]SteeringRecord, error)

	// Send submits a command. It returns promptly: a command is accepted
	// and acted on asynchronously, never executed inline, so a UI's event
	// loop is never blocked by research work.
	Send(ctx context.Context, cmd ResearchCommand) error
}
