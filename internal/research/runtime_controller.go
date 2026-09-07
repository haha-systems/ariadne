package research

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/haha-systems/ariadne/internal/control"
	"github.com/haha-systems/ariadne/internal/invocation"
	"github.com/haha-systems/ariadne/internal/memory"
)

// This file implements control.ResearchController. Everything an interface
// can see or do to a research run passes through these methods.

// Snapshot assembles the presentation projection: runtime-local state
// (which agent is working, whether a pause is pending) combined with counts
// read from shared memory. Memory read failures degrade the snapshot rather
// than failing it — a status view missing a hypothesis count is far better
// than a UI that shows nothing because one query timed out.
func (r *Runtime) Snapshot(ctx context.Context) (control.ResearchSnapshot, error) {
	rc := LoadContext(ctx, r.store, r.project)

	r.mu.Lock()
	defer r.mu.Unlock()

	snap := control.ResearchSnapshot{
		Project:           r.project,
		State:             r.state,
		Cycle:             r.cycle,
		ActiveAgent:       r.activeAgent,
		Agents:            r.agentsLocked(),
		Frontier:          rc.Frontier,
		ActiveHypotheses:  len(rc.ActiveHypotheses),
		RejectedCount:     len(rc.RejectedHypotheses),
		OpenQuestionCount: len(rc.OpenQuestions),
		ConfoundCount:     len(rc.KnownConfounds),
		CurrentExperiment: rc.CurrentExperiment,
		LatestResult:      r.latest,
		QAC:               r.qacSnapshotLocked(),
		MemoryBackend:     r.memoryBackend,
		LastError:         r.lastError,
		TakenAt:           time.Now(),
	}
	if snap.Frontier == "" {
		snap.Frontier = r.frontier
	}
	if snap.LatestResult == "" {
		snap.LatestResult = rc.LastAstraDecision
	}

	// Counting pending steering needs a memory read, done outside the lock
	// would risk an inconsistent snapshot; it is cheap, so it is done in
	// the caller's context before locking.
	snap.PendingSteering = r.pendingSteeringCount

	return snap, nil
}

// agentsLocked returns agent snapshots in configured order.
func (r *Runtime) agentsLocked() []control.AgentSnapshot {
	out := make([]control.AgentSnapshot, 0, len(r.agentOrder))
	seen := make(map[string]bool, len(r.agentOrder))
	for _, name := range r.agentOrder {
		if seen[name] {
			continue
		}
		seen[name] = true
		if snap, ok := r.agents[name]; ok {
			out = append(out, snap)
		}
	}
	return out
}

// qacSnapshotLocked reads the scheduler's quota state.
//
// CallsRemainingKnown is false when no per-session limit is configured:
// "unlimited" and "none left" are opposite situations and must not render
// identically.
func (r *Runtime) qacSnapshotLocked() control.QACSnapshot {
	snap := control.QACSnapshot{Mode: r.qacMode}
	if r.pending != nil {
		p := r.pending.summary
		snap.PendingEscalation = &p
	}
	if r.budget == nil {
		return snap
	}

	b := r.budget.BudgetSnapshot()
	snap.CallsUsed = b.SessionCount
	if b.PerSessionMax > 0 {
		snap.CallsRemaining = b.PerSessionMax - b.SessionCount
		if snap.CallsRemaining < 0 {
			snap.CallsRemaining = 0
		}
		snap.CallsRemainingKnown = true
	}
	if b.Cooldown > 0 && !b.LastEscalation.IsZero() {
		until := b.LastEscalation.Add(b.Cooldown)
		if until.After(time.Now()) {
			snap.CooldownUntil = &until
		}
	}
	return snap
}

// Subscribe streams live runtime events.
func (r *Runtime) Subscribe(ctx context.Context) (<-chan control.Event, error) {
	if r.bus == nil {
		return nil, fmt.Errorf("research: no event bus configured")
	}
	return r.bus.Subscribe(ctx), nil
}

// SubscribeLogs streams live agent output.
func (r *Runtime) SubscribeLogs(ctx context.Context) (<-chan control.AgentLogEntry, error) {
	if r.logs == nil {
		return nil, fmt.Errorf("research: no log bus configured")
	}
	return r.logs.Subscribe(ctx), nil
}

// RecentEvents replays persisted events so an interface opening mid-run has
// immediate context. It never replays a project's whole history: the point
// is orientation, not archaeology.
func (r *Runtime) RecentEvents(ctx context.Context, limit int) ([]control.Event, error) {
	if r.eventReplay == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	return r.eventReplay.Replay(ctx, limit)
}

// AgentLog returns buffered output for one agent, or all agents when agent
// is empty.
func (r *Runtime) AgentLog(ctx context.Context, agent string, limit int) ([]control.AgentLogEntry, error) {
	if r.logs == nil {
		return nil, nil
	}
	return r.logs.History(agent, limit), nil
}

// Escalations returns the folded escalation history, newest last, with any
// currently pending manual approval appended.
func (r *Runtime) Escalations(ctx context.Context, limit int) ([]control.EscalationSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := append([]control.EscalationSummary(nil), r.escalations...)
	if r.pending != nil {
		out = append(out, r.pending.summary)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// Memory projects the configured memory.Store into a display view. It is
// backend-agnostic by construction: the store interface is all it touches,
// and Backend is carried as a label only.
func (r *Runtime) Memory(ctx context.Context) (control.MemoryView, error) {
	view := control.MemoryView{Backend: r.memoryBackend}

	if frontier, err := r.store.Frontier(ctx, r.project); err == nil {
		view.Frontier = toControlRecord(frontier)
	}

	observations, err := r.store.Retrieve(ctx, memory.RetrieveInput{
		Project: r.project, Kinds: []memory.Kind{memory.KindObservation}, TopK: retrieveTopK,
	})
	if err != nil {
		return view, fmt.Errorf("research: memory view: %w", err)
	}
	for _, rec := range observations {
		switch rec.Subject {
		case SubjectOpenQuestion:
			view.OpenQuestions = append(view.OpenQuestions, toControlRecord(rec))
		case SubjectConfound:
			view.Confounds = append(view.Confounds, toControlRecord(rec))
		default:
			view.Observations = append(view.Observations, toControlRecord(rec))
		}
	}

	hypotheses, err := r.store.Retrieve(ctx, memory.RetrieveInput{
		Project: r.project, Kinds: []memory.Kind{memory.KindHypothesis}, TopK: retrieveTopK,
	})
	if err != nil {
		return view, fmt.Errorf("research: memory view: %w", err)
	}
	for _, rec := range hypotheses {
		if rec.State == memory.StateRejected || rec.State == memory.StateDeprecated {
			view.RejectedHypotheses = append(view.RejectedHypotheses, toControlRecord(rec))
			continue
		}
		view.ActiveHypotheses = append(view.ActiveHypotheses, toControlRecord(rec))
	}

	decisions, err := r.store.Retrieve(ctx, memory.RetrieveInput{
		Project: r.project, Kinds: []memory.Kind{memory.KindDecision}, TopK: retrieveTopK,
	})
	if err != nil {
		return view, fmt.Errorf("research: memory view: %w", err)
	}
	for _, rec := range decisions {
		view.Decisions = append(view.Decisions, toControlRecord(rec))
		if rec.AgentID == AgentAstra {
			view.LatestAstraResult = toControlRecord(rec)
		}
	}

	return view, nil
}

// Steering returns the project's steering records, newest last.
func (r *Runtime) Steering(ctx context.Context) ([]control.SteeringRecord, error) {
	recs, err := r.store.Retrieve(ctx, memory.RetrieveInput{
		Project: r.project, Kinds: []memory.Kind{memory.KindSteering}, TopK: retrieveTopK,
	})
	if err != nil {
		return nil, fmt.Errorf("research: steering: %w", err)
	}

	out := make([]control.SteeringRecord, 0, len(recs))
	pending := 0
	for _, rec := range recs {
		record := control.SteeringRecord{
			ID:            rec.ID,
			Project:       rec.Project,
			ReceivedAt:    rec.CreatedAt,
			Message:       rec.Summary,
			Source:        rec.AgentID,
			Status:        steeringStatus(rec.State),
			CycleReceived: -1,
			CycleApplied:  -1,
		}
		if record.Status == control.SteeringPending {
			pending++
		}
		out = append(out, record)
	}

	r.mu.Lock()
	r.pendingSteeringCount = pending
	r.mu.Unlock()

	return out, nil
}

// steeringStatus maps a memory lifecycle state onto the steering
// vocabulary. The two vocabularies are kept separate because a steering
// message's lifecycle is a user-facing concept, not a storage detail.
func steeringStatus(state string) string {
	switch state {
	case memory.StateAccepted:
		return control.SteeringApplied
	case memory.StateRejected:
		return control.SteeringCancelled
	case memory.StateDeprecated:
		return control.SteeringSuperseded
	default:
		return control.SteeringPending
	}
}

func toControlRecord(rec memory.Record) control.MemoryRecord {
	return control.MemoryRecord{
		ID:           rec.ID,
		Kind:         string(rec.Kind),
		Subject:      rec.Subject,
		Summary:      rec.Summary,
		AgentID:      rec.AgentID,
		State:        rec.State,
		Confidence:   rec.Confidence,
		EvidenceRefs: rec.EvidenceRefs,
		CreatedAt:    rec.CreatedAt,
	}
}

// Send validates and records a command. It never performs research work
// inline: a command either mutates small runtime state (pause, resume,
// approval) or writes one memory record (steering), and the research loop
// picks the effect up at its own next boundary. A UI's event loop is
// therefore never blocked behind an agent invocation.
func (r *Runtime) Send(ctx context.Context, cmd control.ResearchCommand) error {
	if err := control.Validate(cmd); err != nil {
		return err
	}
	r.publish(invocation.EventCommandReceived, map[string]any{
		"project": r.project, "command": control.CommandName(cmd),
	})

	switch c := cmd.(type) {
	case control.SteerCommand:
		return r.steer(ctx, c)
	case control.PauseCommand:
		return r.pause()
	case control.ResumeCommand:
		return r.resume()
	case control.ApproveEscalationCommand:
		return r.decideEscalation(c.EscalationID, true, "approved by user")
	case control.DenyEscalationCommand:
		reason := strings.TrimSpace(c.Reason)
		if reason == "" {
			reason = "denied by user"
		}
		return r.decideEscalation(c.EscalationID, false, reason)
	default:
		return fmt.Errorf("%w: %T", control.ErrNotSupported, cmd)
	}
}

// steer persists a user instruction as a pending steering record. It is
// written to shared memory rather than held in the interface so it survives
// a UI restart and remains part of the auditable research record.
func (r *Runtime) steer(ctx context.Context, cmd control.SteerCommand) error {
	source := strings.TrimSpace(cmd.Source)
	if source == "" {
		source = "user"
	}

	rec, err := r.store.Observe(ctx, memory.ObserveInput{
		Project: r.project,
		Kind:    memory.KindSteering,
		Subject: "steering",
		Summary: strings.TrimSpace(cmd.Message),
		AgentID: source,
	})
	if err != nil {
		return fmt.Errorf("research: record steering: %w", err)
	}

	r.mu.Lock()
	r.pendingSteeringCount++
	cycle := r.cycle
	r.mu.Unlock()

	r.publish(invocation.EventSteeringReceived, map[string]any{
		"project": r.project, "steering_id": rec.ID,
		"message": cmd.Message, "source": source, "cycle": cycle,
	})
	return nil
}

// pause requests a stop at the next safe boundary. It is idempotent, and it
// deliberately does not stop anything immediately: see CycleStarting.
func (r *Runtime) pause() error {
	r.mu.Lock()
	if r.state == control.RunStateStopped || r.state == control.RunStateFailed {
		r.mu.Unlock()
		return control.ErrNotRunning
	}
	already := r.pauseRequested
	r.pauseRequested = true
	if !already && r.state == control.RunStateRunning {
		r.state = control.RunStatePauseRequested
	}
	r.mu.Unlock()

	if !already {
		r.publish(invocation.EventCommandReceived, map[string]any{
			"project": r.project, "command": "pause", "state": string(control.RunStatePauseRequested),
		})
	}
	return nil
}

// resume releases a paused loop. Resuming discards nothing: the frontier,
// pending steering, quota, and event history are all held outside this
// runtime, so a resumed loop simply continues from persisted state.
func (r *Runtime) resume() error {
	r.mu.Lock()
	if !r.pauseRequested {
		r.mu.Unlock()
		return nil // already running; resuming twice is not an error
	}
	r.pauseRequested = false
	waiters := r.resumeCh
	r.resumeCh = make(chan struct{})
	if r.state == control.RunStatePauseRequested {
		r.state = control.RunStateRunning
	}
	r.mu.Unlock()

	close(waiters)
	return nil
}

// decideEscalation relays a human's answer to a blocked manual approval.
func (r *Runtime) decideEscalation(id string, approved bool, reason string) error {
	r.mu.Lock()
	p := r.pending
	if p == nil {
		r.mu.Unlock()
		return control.ErrNoSuchEscalation
	}
	if p.summary.ID != id {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", control.ErrNoSuchEscalation, id)
	}
	r.mu.Unlock()

	select {
	case p.decided <- approvalDecision{approved: approved, reason: reason}:
		return nil
	default:
		// Already decided — a double key-press, or a race with the run
		// ending. Not an error worth showing a user.
		return nil
	}
}
