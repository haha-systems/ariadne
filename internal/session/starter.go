package session

import "context"

// Starter is an optional capability of a Host: it can begin new work, not
// only show work already running.
//
// It is separate from Host because starting work is a strictly larger
// privilege than watching it, and a host that only observes — a read-only
// remote client, say — should be unable to spend an agent invocation rather
// than merely declining to. An interface asks whether its host implements
// this, and offers the key only if it does.
type Starter interface {
	// StartTask queues a task described in prose and returns its title. It
	// returns promptly: the work is queued, never executed inline, so a
	// UI's event loop is never blocked by an agent run.
	StartTask(ctx context.Context, prompt string) (string, error)
}

// AsStarter reports whether a host can start work, and returns it as a
// Starter when it can.
func AsStarter(h Host) (Starter, bool) {
	s, ok := h.(Starter)
	return s, ok
}
