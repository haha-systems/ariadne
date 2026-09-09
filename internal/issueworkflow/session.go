package issueworkflow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/haha-systems/ariadne/internal/control"
	"github.com/haha-systems/ariadne/internal/domain"
	"github.com/haha-systems/ariadne/internal/session"
)

// This file makes one issue-workflow run a session, so the harness shows a
// claimed issue and a research programme in the same list.
//
// The issue workflow had no observability beyond structured logs, because
// nothing ever watched it live. A run session adds the two buses the
// research runtime already uses, and nothing else: no new lifecycle, no new
// state machine, no second copy of run state. The Run and ProofBundle the
// workflow already owns remain the source of truth, and the session is a
// projection over them.

// logHistory is how many output lines one run keeps for a UI attaching
// mid-run. It is bounded because a chatty provider must not be able to grow
// a long-lived process without limit.
const logHistory = 2000

// RunSession is a session.Controller over a single issue-workflow run.
type RunSession struct {
	id    string
	title string
	repo  string

	bus  *control.Bus
	logs *control.LogBus

	mu        sync.Mutex
	state     control.RunState
	provider  string
	persona   string
	summary   string
	lastError string
	startedAt time.Time
	updatedAt time.Time
}

// NewRunSession creates a session for a run that is about to start.
func NewRunSession(run *domain.Run, task *domain.Task, repo string) *RunSession {
	now := time.Now()
	title := task.Title
	if title == "" {
		title = task.ID
	}

	return &RunSession{
		id:        "issue:" + run.ID,
		title:     title,
		repo:      repo,
		bus:       control.NewBus(),
		logs:      control.NewLogBus(logHistory),
		state:     control.RunStateIdle,
		provider:  run.Provider,
		summary:   "claimed " + task.ID,
		startedAt: now,
		updatedAt: now,
	}
}

// ID is the session's stable identifier.
func (s *RunSession) ID() string { return s.id }

// Bus exposes the event bus so the workflow can narrate the run.
func (s *RunSession) Bus() *control.Bus { return s.bus }

// Logs exposes the log bus, whose Writer can be handed to a provider to
// capture agent output.
func (s *RunSession) Logs() *control.LogBus { return s.logs }

// Started marks the run as executing under a provider and persona.
func (s *RunSession) Started(provider, persona string) {
	s.mu.Lock()
	s.state = control.RunStateRunning
	s.provider = provider
	s.persona = persona
	s.summary = "running on " + provider
	s.updatedAt = time.Now()
	s.mu.Unlock()

	s.bus.Log("run_started", map[string]any{
		"run_id": s.id, "provider": provider, "persona": persona,
	})
}

// Progress records a one-line description of what the run is doing.
func (s *RunSession) Progress(stage string) {
	s.mu.Lock()
	s.summary = stage
	s.updatedAt = time.Now()
	s.mu.Unlock()

	s.bus.Log("run_progress", map[string]any{"run_id": s.id, "stage": stage})
}

// Finished records a terminal outcome. A failed run stays registered: the
// worktree is preserved on failure, and a run you cannot inspect afterwards
// is exactly the one you most needed to see.
func (s *RunSession) Finished(err error) {
	s.mu.Lock()
	if err != nil {
		s.state = control.RunStateFailed
		s.lastError = err.Error()
		s.summary = "failed"
	} else {
		s.state = control.RunStateStopped
		s.summary = "complete"
	}
	s.updatedAt = time.Now()
	s.mu.Unlock()

	fields := map[string]any{"run_id": s.id}
	if err != nil {
		fields["error"] = err.Error()
	}
	s.bus.Log("run_finished", fields)
}

// Close releases the run's buses. It is called once the run is fully
// finished and no interface is expected to attach again.
func (s *RunSession) Close() {
	s.bus.Close()
	s.logs.Close()
}

// Describe implements session.Controller.
func (s *RunSession) Describe(context.Context) (session.Descriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return session.Descriptor{
		ID:        s.id,
		Kind:      session.KindIssue,
		Title:     s.title,
		Repo:      s.repo,
		State:     s.state,
		Summary:   s.summary,
		StartedAt: s.startedAt,
		UpdatedAt: s.updatedAt,
		LastError: s.lastError,
	}, nil
}

// Capabilities implements session.Controller. An issue run streams output
// but has no steering, escalation, or memory surface: it executes a task
// that was fully specified before it started.
func (s *RunSession) Capabilities() session.Capabilities {
	return session.NewCapabilities(session.CapAgentLog)
}

// Snapshot implements session.Controller, projecting the run into the
// shared presentation shape so one status view renders every session kind.
func (s *RunSession) Snapshot(context.Context) (control.ResearchSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent := control.AgentSnapshot{
		Name:        s.provider,
		Provider:    s.provider,
		Role:        s.persona,
		State:       control.AgentIdle,
		CurrentTask: s.summary,
		StartedAt:   s.startedAt,
	}
	switch s.state {
	case control.RunStateRunning:
		agent.State = control.AgentWorking
	case control.RunStateFailed:
		agent.State = control.AgentFailed
	}

	return control.ResearchSnapshot{
		Project:      s.title,
		State:        s.state,
		ActiveAgent:  s.provider,
		Agents:       []control.AgentSnapshot{agent},
		LatestResult: s.summary,
		LastError:    s.lastError,
		TakenAt:      time.Now(),
	}, nil
}

// Subscribe implements session.Controller.
func (s *RunSession) Subscribe(ctx context.Context) (<-chan control.Event, error) {
	return s.bus.Subscribe(ctx), nil
}

// SubscribeLogs implements session.Controller.
func (s *RunSession) SubscribeLogs(ctx context.Context) (<-chan control.AgentLogEntry, error) {
	return s.logs.Subscribe(ctx), nil
}

// RecentEvents implements session.Controller. A run's events are not
// persisted separately from the log, so there is no replay beyond what a
// live subscriber has seen.
func (s *RunSession) RecentEvents(context.Context, int) ([]control.Event, error) {
	return nil, nil
}

// AgentLog implements session.Controller.
func (s *RunSession) AgentLog(_ context.Context, agent string, limit int) ([]control.AgentLogEntry, error) {
	return s.logs.History(agent, limit), nil
}

// Escalations implements session.Controller: an issue run has no QAC.
func (s *RunSession) Escalations(context.Context, int) ([]control.EscalationSummary, error) {
	return nil, session.Unsupported("escalations")
}

// Memory implements session.Controller: an issue run has no research memory.
func (s *RunSession) Memory(context.Context) (control.MemoryView, error) {
	return control.MemoryView{}, session.Unsupported("memory")
}

// Steering implements session.Controller: an issue run is not steerable.
func (s *RunSession) Steering(context.Context) ([]control.SteeringRecord, error) {
	return nil, session.Unsupported("steering")
}

// Send implements session.Controller. Every command is refused rather than
// silently ignored, so a UI that offers steering against the wrong session
// fails loudly in review instead of quietly in production.
func (s *RunSession) Send(_ context.Context, cmd control.ResearchCommand) error {
	if err := control.Validate(cmd); err != nil {
		return err
	}
	return fmt.Errorf("%w: issue run does not accept %s", control.ErrNotSupported, control.CommandName(cmd))
}

var _ session.Controller = (*RunSession)(nil)
