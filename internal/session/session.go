// Package session is Ariadne's unifying abstraction: the thing a harness
// shows you one of, and lets you switch between.
//
// Ariadne grew two workflows that had nothing structurally in common at the
// interface layer — the issue workflow (poll, claim, route, run, prove) and
// the research programme (cycle, escalate, remember, steer). Both are, from
// a user's seat, the same shape: a long-running unit of agent work with
// live output, a lifecycle, and a small set of things you may do to it. A
// session is that shape, named.
//
// The package holds no state and starts nothing. It depends only on
// internal/control, which supplies the shared vocabulary of events, logs,
// and commands, so a UI can import both without pulling in providers,
// schedulers, or memory backends. Each workflow supplies an adapter that
// satisfies Controller; neither workflow learns about the other, and the
// harness learns about neither.
package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/haha-systems/ariadne/internal/control"
)

// Kind identifies which workflow produced a session. It exists for
// presentation and filtering only. The harness must never branch on Kind to
// decide what a session can do — that is what Capabilities is for, and a
// new workflow should be addable without editing a single switch.
type Kind string

const (
	// KindResearch is a research programme: routine and frontier agents
	// cycling under QAC.
	KindResearch Kind = "research"
	// KindIssue is one issue-workflow run: a claimed task executed in a
	// worktree and landed with proof.
	KindIssue Kind = "issue"
	// KindChat is a direct interactive session against a single provider,
	// with no work source in front of it.
	KindChat Kind = "chat"
)

// Display renders a Kind for a session list.
func (k Kind) Display() string {
	if k == "" {
		return "?"
	}
	return strings.ToUpper(string(k))
}

// Descriptor is the one-line identity of a session: enough to render it in
// a switcher without opening it. It is a projection — rebuilding it from
// the owning runtime must always be safe.
type Descriptor struct {
	// ID is unique across every session in a host and stable for the
	// session's lifetime.
	ID string
	// Kind is the originating workflow.
	Kind Kind
	// Title is the human label: a project name, an issue title, a prompt.
	Title string
	// Repo is the working directory the session acts on, when it has one.
	Repo string

	// State reuses the research lifecycle vocabulary, which already draws
	// the distinctions a session list needs — notably that a requested
	// pause and an achieved one are not the same thing.
	State control.RunState

	// Summary is a short line describing what the session is doing right
	// now, refreshed as it works.
	Summary string

	StartedAt time.Time
	UpdatedAt time.Time

	// LastError is the most recent recoverable error, surfaced so a list
	// can flag a limping session that has not actually failed.
	LastError string
}

// Active reports whether the session is doing work or intends to resume.
// A switcher uses it to sort live sessions above finished ones.
func (d Descriptor) Active() bool {
	switch d.State {
	case control.RunStateRunning, control.RunStatePauseRequested, control.RunStatePaused:
		return true
	default:
		return false
	}
}

// Capability names one optional surface a session may support. The harness
// asks a controller what it can do rather than inferring it from Kind, so
// adding a workflow never means editing the interface's switches.
type Capability string

const (
	// CapSteer means the session accepts control.SteerCommand.
	CapSteer Capability = "steer"
	// CapPauseResume means the session honours pause and resume.
	CapPauseResume Capability = "pause_resume"
	// CapEscalations means the session exposes QAC escalation records and
	// can act on approvals.
	CapEscalations Capability = "escalations"
	// CapMemory means the session exposes a research memory view.
	CapMemory Capability = "memory"
	// CapAgentLog means the session streams per-agent output.
	CapAgentLog Capability = "agent_log"
)

// Capabilities is a set of supported surfaces.
type Capabilities map[Capability]bool

// Has reports whether the capability is supported. The nil map is a valid
// empty set, so a minimal controller can return nil.
func (c Capabilities) Has(cap Capability) bool { return c[cap] }

// NewCapabilities builds a set from a list.
func NewCapabilities(caps ...Capability) Capabilities {
	set := make(Capabilities, len(caps))
	for _, c := range caps {
		set[c] = true
	}
	return set
}

// Errors a host or controller may return.
var (
	// ErrNoSuchSession means the referenced session is unknown to the host.
	ErrNoSuchSession = errors.New("session: no such session")
	// ErrDuplicateSession means a session with that ID is already registered.
	ErrDuplicateSession = errors.New("session: duplicate session id")
	// ErrClosed means the host has shut down.
	ErrClosed = errors.New("session: host is closed")
)

// Controller is everything a UI may do with one session. It is the
// generalisation of control.ResearchController: read-mostly, plus a single
// Send that carries a closed set of commands, which keeps the set of
// UI-initiated effects finite and reviewable.
//
// Optional surfaces return control.ErrNotSupported when the corresponding
// Capability is absent, so a caller may either check first or just ask.
type Controller interface {
	// Describe returns the session's current one-line identity.
	Describe(ctx context.Context) (Descriptor, error)

	// Capabilities reports which optional surfaces this session supports.
	// It is not context-taking: capabilities are a property of the session's
	// construction and must not change under a running UI.
	Capabilities() Capabilities

	// Snapshot returns a consistent projection of session state.
	Snapshot(ctx context.Context) (control.ResearchSnapshot, error)

	// Subscribe streams structured events until ctx is cancelled or the
	// session ends. Implementations drop events for a slow subscriber
	// rather than applying backpressure to the work.
	Subscribe(ctx context.Context) (<-chan control.Event, error)

	// SubscribeLogs streams agent output under the same non-blocking
	// contract as Subscribe.
	SubscribeLogs(ctx context.Context) (<-chan control.AgentLogEntry, error)

	// RecentEvents returns up to limit recent persisted events, so a UI
	// opening mid-run has context instead of an empty screen.
	RecentEvents(ctx context.Context, limit int) ([]control.Event, error)

	// AgentLog returns recent buffered output for one agent, or every agent
	// when agent is "".
	AgentLog(ctx context.Context, agent string, limit int) ([]control.AgentLogEntry, error)

	// Escalations returns recent QAC escalation records, newest last.
	Escalations(ctx context.Context, limit int) ([]control.EscalationSummary, error)

	// Memory returns the structured view of research memory.
	Memory(ctx context.Context) (control.MemoryView, error)

	// Steering returns the session's steering records, newest last.
	Steering(ctx context.Context) ([]control.SteeringRecord, error)

	// Send submits a command. It returns promptly: a command is accepted
	// and acted on asynchronously, never executed inline, so a UI's event
	// loop is never blocked by agent work.
	Send(ctx context.Context, cmd control.ResearchCommand) error
}

// Unsupported returns the error an optional surface should give when its
// capability is absent, naming the surface so the message is actionable.
func Unsupported(surface string) error {
	return fmt.Errorf("%w: session does not support %s", control.ErrNotSupported, surface)
}
