package research

import (
	"context"
	"fmt"
	"time"

	"github.com/haha-systems/ariadne/internal/session"
)

// This file adapts a research Runtime to session.Controller, so a research
// programme becomes one session among others in the harness.
//
// The adapter is thin on purpose. Runtime already implements every
// read and the single Send that session.Controller requires — the same
// methods control.ResearchController has always needed — so unification
// costs the research workflow no behavioural change at all. What the
// adapter adds is identity: a stable ID, a title, and the capability set
// that tells the interface which surfaces to offer.

// Session wraps a Runtime as a session.Controller.
type Session struct {
	*Runtime

	id        string
	repo      string
	startedAt time.Time
}

// NewSession adapts a running or idle Runtime for the harness. The repo is
// the working directory the programme acts on, shown in the session list;
// it may be empty.
func NewSession(id, repo string, rt *Runtime) (*Session, error) {
	if rt == nil {
		return nil, fmt.Errorf("research: nil runtime for session %s", id)
	}
	if id == "" {
		id = "research:" + rt.project
	}
	return &Session{Runtime: rt, id: id, repo: repo, startedAt: time.Now()}, nil
}

// Describe implements session.Controller. It derives the descriptor from a
// live snapshot rather than caching one, so the session list can never
// disagree with the session's own status view.
func (s *Session) Describe(ctx context.Context) (session.Descriptor, error) {
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return session.Descriptor{}, fmt.Errorf("research: describing session %s: %w", s.id, err)
	}

	summary := snap.Frontier
	if snap.ActiveAgent != "" {
		summary = fmt.Sprintf("cycle %d · %s", snap.Cycle, snap.ActiveAgent)
	}

	return session.Descriptor{
		ID:        s.id,
		Kind:      session.KindResearch,
		Title:     snap.Project,
		Repo:      s.repo,
		State:     snap.State,
		Summary:   summary,
		StartedAt: s.startedAt,
		UpdatedAt: snap.TakenAt,
		LastError: snap.LastError,
	}, nil
}

// Capabilities implements session.Controller. A research programme supports
// every optional surface the harness knows about — it is the workflow the
// surfaces were designed around.
func (s *Session) Capabilities() session.Capabilities {
	return session.NewCapabilities(
		session.CapSteer,
		session.CapPauseResume,
		session.CapEscalations,
		session.CapMemory,
		session.CapAgentLog,
	)
}

// Compile-time proof that the adapter satisfies the harness contract.
var _ session.Controller = (*Session)(nil)
