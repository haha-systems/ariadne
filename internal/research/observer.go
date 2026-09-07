package research

import (
	"context"

	"github.com/haha-systems/ariadne/internal/invocation"
)

// Observer lets a host watch and gate the research loop without the loop
// knowing anything about that host. It is how the runtime implements pause
// and how an interface learns which agent is active.
//
// Both interfaces here are optional: AriLoop with neither set behaves
// exactly as the CLI has always behaved. That is the point — the research
// loop must stay fully usable with no interface attached.
type Observer interface {
	// CycleStarting is called before each cycle's prompt is built. It is
	// the loop's one safe boundary: an implementation may block here to
	// hold research paused, because nothing is in flight at this point.
	// Returning an error stops the run cleanly.
	CycleStarting(ctx context.Context, project string, cycle int) error

	// AgentStarted reports that an agent invocation is beginning.
	AgentStarted(project, agent, task string)

	// AgentFinished reports an invocation's outcome. Exactly one of
	// envelope/err is meaningful, per the usual Go convention.
	AgentFinished(project, agent string, envelope invocation.Envelope, err error)
}

// SteeringSource supplies pending user instructions to a research turn and
// records that they were delivered.
//
// Pending and MarkApplied are separate calls, in that order around the
// invocation, so an instruction is only marked applied once it has actually
// reached an agent. A crash between them re-delivers the instruction, which
// is the right failure direction: a steering message repeated is a nuisance,
// a steering message silently lost is a user whose instruction was ignored.
type SteeringSource interface {
	Pending(ctx context.Context, project string) ([]SteeringMessage, error)
	MarkApplied(ctx context.Context, project string, ids []string, cycle int) error
}

// SteeringMessage is one pending instruction.
type SteeringMessage struct {
	ID      string
	Message string
}

// steeringTexts extracts the message bodies for prompt rendering.
func steeringTexts(msgs []SteeringMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Message)
	}
	return out
}

func steeringIDs(msgs []SteeringMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}
