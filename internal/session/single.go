package session

import (
	"context"
	"fmt"

	"github.com/haha-systems/ariadne/internal/control"
)

// This file bridges the pre-session world to the harness.
//
// A control.ResearchController is structurally everything a Controller is,
// minus identity: it has every read and the same Send. Adapt supplies the
// missing identity so anything that already spoke the old interface — the
// research runtime, a test double, a future remote client — becomes a
// session without being rewritten. Single then presents one such session as
// a Host, so the harness has exactly one way to open a session no matter
// how many there are.

// adapted gives a control.ResearchController a descriptor and capabilities.
type adapted struct {
	control.ResearchController
	desc Descriptor
	caps Capabilities
}

// Adapt turns a control.ResearchController into a Controller. The
// descriptor supplies identity; capabilities declare which optional
// surfaces the underlying controller actually services.
//
// Describe re-reads State and LastError from a live snapshot, so an adapted
// session's lifecycle stays truthful even though the descriptor it was
// registered with is a fixed value.
func Adapt(desc Descriptor, caps Capabilities, c control.ResearchController) (Controller, error) {
	if c == nil {
		return nil, fmt.Errorf("session: nil controller for %s", desc.ID)
	}
	if desc.ID == "" {
		return nil, fmt.Errorf("%w: empty session id", control.ErrInvalidCommand)
	}
	return &adapted{ResearchController: c, desc: desc, caps: caps}, nil
}

func (a *adapted) Capabilities() Capabilities { return a.caps }

func (a *adapted) Describe(ctx context.Context) (Descriptor, error) {
	desc := a.desc
	snap, err := a.Snapshot(ctx)
	if err != nil {
		// A failed snapshot is not a failed session. Report the last known
		// identity and surface the error in the row rather than removing
		// the session from the list the user is trying to look at.
		desc.LastError = err.Error()
		return desc, nil
	}

	desc.State = snap.State
	desc.LastError = snap.LastError
	desc.UpdatedAt = snap.TakenAt
	if desc.Title == "" {
		desc.Title = snap.Project
	}
	if snap.ActiveAgent != "" {
		desc.Summary = fmt.Sprintf("cycle %d · %s", snap.Cycle, snap.ActiveAgent)
	}
	return desc, nil
}

// ResearchCapabilities is the surface set a full research programme
// supports. It is named here so the CLI and the harness cannot drift on
// what a research session can do.
func ResearchCapabilities() Capabilities {
	return NewCapabilities(CapSteer, CapPauseResume, CapEscalations, CapMemory, CapAgentLog)
}

// Single presents one controller as a Host, for the case where a command
// attached an interface to exactly one session. The harness then has a
// single code path: it always talks to a Host, whether it is showing one
// session or twenty.
func Single(desc Descriptor, controller Controller) (Host, error) {
	if controller == nil {
		return nil, fmt.Errorf("session: nil controller for %s", desc.ID)
	}
	r := NewRegistry()
	if err := r.Register(desc, controller); err != nil {
		return nil, err
	}
	return r, nil
}

var _ Controller = (*adapted)(nil)
