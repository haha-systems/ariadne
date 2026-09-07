package research

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/haha-systems/ariadne/internal/control"
	"github.com/haha-systems/ariadne/internal/invocation"
	"github.com/haha-systems/ariadne/internal/memory"
	"github.com/haha-systems/ariadne/internal/scheduler"
)

// Runtime hosts one project's research loop and exposes it through
// control.ResearchController.
//
// It owns no research state of its own. The frontier, hypotheses, and
// escalation records live in memory.Store; the quota lives in the
// scheduler's Budget; the durable event history lives in run.jsonl. What
// Runtime maintains is a *projection* folded from the loop's own event
// stream, plus the small amount of genuinely runtime-local state an
// interface needs — whether a pause has been requested, which agent is
// currently working, and which escalation is awaiting a human.
//
// Folding the projection from events rather than from direct callbacks is
// deliberate: it means the snapshot can only ever show things that also
// reached the durable log, so a UI cannot display research history that no
// audit trail records.
type Runtime struct {
	project   string
	loop      *AriLoop
	store     memory.Store
	bus       *control.Bus
	logs      *control.LogBus
	maxCycles int

	// budget is the scheduler's quota reporter, when one is available.
	budget budgetReporter
	// qacMode is the configured control mode, shown in the status header.
	qacMode string
	// memoryBackend names the store implementation, as display metadata.
	memoryBackend string
	// eventReplay supplies persisted events from previous runs.
	eventReplay EventReplayer

	// agentOrder preserves the configured display order of agents.
	agentOrder []string

	mu sync.Mutex

	state       control.RunState
	cycle       int
	activeAgent string
	agents      map[string]control.AgentSnapshot
	lastError   string

	pauseRequested  bool
	pausedAnnounced bool
	resumeCh        chan struct{}

	// escalations is the folded escalation history, newest last.
	escalations []control.EscalationSummary
	// lastScore holds the most recent escalation_scored factors, used to
	// enrich a pending manual approval: the scheduler's Approver callback
	// receives only the request context, not the score behind it.
	lastScore control.EscalationSummary

	pending              *pendingEscalation
	pendingSteeringCount int
	nextID               int
	frontier             string
	latest               string
	doneErr              error
	done                 chan struct{}
	startOnce            sync.Once
}

// pendingEscalation is one manual-mode approval awaiting a human decision.
type pendingEscalation struct {
	summary control.EscalationSummary
	// decided carries the human's answer back to the blocked Approver call.
	decided chan approvalDecision
}

type approvalDecision struct {
	approved bool
	reason   string
}

// EventReplayer supplies previously persisted events so an interface
// attaching to a run has immediate context rather than a blank screen. It
// is an interface so the runtime does not have to know that the durable log
// happens to be JSONL on disk.
type EventReplayer interface {
	Replay(ctx context.Context, limit int) ([]control.Event, error)
}

// RuntimeConfig wires a Runtime. Loop, Memory, and Bus are required.
type RuntimeConfig struct {
	Project   string
	Loop      *AriLoop
	Memory    memory.Store
	Bus       *control.Bus
	Logs      *control.LogBus
	MaxCycles int

	QACMode       string
	MemoryBackend string
	EventReplay   EventReplayer

	// Agents are the static descriptors for each configured agent, in
	// display order.
	Agents []control.AgentSnapshot
}

var (
	_ control.ResearchController = (*Runtime)(nil)
	_ Observer                   = (*Runtime)(nil)
	_ SteeringSource             = (*Runtime)(nil)
	_ scheduler.Approver         = (*Runtime)(nil)
)

// NewRuntime constructs a Runtime and attaches it to its loop as the
// loop's Observer and SteeringSource.
func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.Logs == nil {
		cfg.Logs = control.NewLogBus(2000)
	}
	if cfg.MaxCycles <= 0 {
		cfg.MaxCycles = 20
	}

	rt := &Runtime{
		project:       cfg.Project,
		loop:          cfg.Loop,
		store:         cfg.Memory,
		bus:           cfg.Bus,
		logs:          cfg.Logs,
		maxCycles:     cfg.MaxCycles,
		qacMode:       cfg.QACMode,
		memoryBackend: cfg.MemoryBackend,
		eventReplay:   cfg.EventReplay,
		state:         control.RunStateIdle,
		agents:        make(map[string]control.AgentSnapshot, len(cfg.Agents)),
		resumeCh:      make(chan struct{}),
		done:          make(chan struct{}),
	}
	for _, a := range cfg.Agents {
		if a.State == "" {
			a.State = control.AgentIdle
		}
		rt.agents[a.Name] = a
		rt.agentOrder = append(rt.agentOrder, a.Name)
	}

	if cfg.Loop != nil {
		cfg.Loop.Observer = rt
		cfg.Loop.Steering = rt
		if reporter, ok := cfg.Loop.Decider.(budgetReporter); ok {
			rt.budget = reporter
		}
	}
	return rt
}

// Start runs the research loop in the background and begins folding its
// events into the projection. It returns immediately; use Wait or Done to
// observe completion.
func (r *Runtime) Start(ctx context.Context) {
	r.startOnce.Do(func() {
		go r.fold(ctx)

		go func() {
			defer close(r.done)

			r.setState(control.RunStateRunning)
			err := r.loop.Run(ctx, r.project, r.maxCycles)

			r.mu.Lock()
			r.doneErr = err
			if err != nil && ctx.Err() == nil {
				r.state = control.RunStateFailed
				r.lastError = err.Error()
			} else {
				r.state = control.RunStateStopped
			}
			r.activeAgent = ""
			for name, a := range r.agents {
				if a.State == control.AgentWorking {
					a.State = control.AgentIdle
					r.agents[name] = a
				}
			}
			r.mu.Unlock()

			// A blocked approval must not outlive the run, or a UI would
			// wait forever on an escalation nothing will ever act on.
			r.failPendingApproval("research run ended")
		}()
	})
}

// Done is closed when the research loop has finished.
func (r *Runtime) Done() <-chan struct{} { return r.done }

func (r *Runtime) setState(state control.RunState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = state
}

// Err returns the loop's terminal error, if any. Call after Done closes.
func (r *Runtime) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.doneErr
}

// fold consumes the runtime's own event stream and maintains the
// presentation projection from it.
func (r *Runtime) fold(ctx context.Context) {
	if r.bus == nil {
		return
	}
	for ev := range r.bus.Subscribe(ctx) {
		r.apply(ev)
	}
}

func (r *Runtime) apply(ev control.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch ev.Name {
	case invocation.EventResearchCycleStarted:
		if c, ok := ev.Number("cycle"); ok {
			r.cycle = int(c)
		}

	case invocation.EventEscalationScored:
		// The loop emits escalation_requested before escalation_scored, so
		// the record already exists by the time its factors arrive: fill
		// them in rather than expecting them up front. The stashed copy
		// serves the manual-approval path, where the scheduler's Approver
		// callback hands over only the request context, not the score.
		r.lastScore = control.EscalationSummary{
			Score:        numberOr(ev, "score", 0),
			Uncertainty:  numberOr(ev, "score_uncertainty", 0),
			Importance:   numberOr(ev, "score_importance", 0),
			ExpectedGain: numberOr(ev, "score_expected_gain", 0),
		}
		if dup, ok := ev.Fields["duplicate_of_denied"].(bool); ok {
			r.lastScore.Duplicate = dup
		}
		r.scoreLatestPendingLocked(r.lastScore)

	case invocation.EventEscalationRequested:
		summary := control.EscalationSummary{
			ID:              r.newIDLocked("esc"),
			RequestedAt:     ev.Time,
			RequestingAgent: AgentAri,
			TargetAgent:     AgentAstra,
			Question:        ev.String("question"),
			Reason:          ev.String("reason"),
			Decision:        control.DecisionPending,
			ScarcityClass:   "rare",
		}
		if n, ok := ev.Number("failed_local_attempts"); ok {
			summary.FailedLocalAttempts = int(n)
		}
		if b, ok := ev.Fields["cheaper_test_available"].(bool); ok {
			summary.CheaperTestAvailable = b
		}
		summary.Score = r.lastScore.Score
		summary.Uncertainty = r.lastScore.Uncertainty
		summary.Importance = r.lastScore.Importance
		summary.ExpectedGain = r.lastScore.ExpectedGain
		summary.Duplicate = r.lastScore.Duplicate
		r.escalations = appendBoundedSlice(r.escalations, summary, 200)

	case invocation.EventEscalationApproved:
		r.decideLatestLocked(control.DecisionApproved, ev.String("reason"), ev.Time)

	case invocation.EventEscalationDenied:
		r.decideLatestLocked(control.DecisionDenied, ev.String("reason"), ev.Time)

	case invocation.EventInvocationFailed:
		if msg := ev.String("error"); msg != "" {
			r.lastError = msg
		}
	case invocation.EventMemoryWriteFailed:
		if msg := ev.String("error"); msg != "" {
			r.lastError = "memory write failed: " + msg
		}
	}
}

// decideLatestLocked stamps the newest pending escalation with its outcome.
// The loop decides escalations strictly one at a time, so the newest
// pending record is always the one this decision refers to.
func (r *Runtime) decideLatestLocked(decision, reason string, at time.Time) {
	for i := len(r.escalations) - 1; i >= 0; i-- {
		if r.escalations[i].Decision != control.DecisionPending {
			continue
		}
		r.escalations[i].Decision = decision
		r.escalations[i].DecisionReason = reason
		r.escalations[i].DecidedAt = at
		r.escalations[i].Budget = r.qacSnapshotLocked()
		return
	}
}

// scoreLatestPendingLocked copies scoring factors onto the newest undecided
// escalation. The loop decides escalations strictly one at a time, so the
// newest pending record is always the one being scored.
func (r *Runtime) scoreLatestPendingLocked(score control.EscalationSummary) {
	for i := len(r.escalations) - 1; i >= 0; i-- {
		if r.escalations[i].Decision != control.DecisionPending {
			continue
		}
		r.escalations[i].Score = score.Score
		r.escalations[i].Uncertainty = score.Uncertainty
		r.escalations[i].Importance = score.Importance
		r.escalations[i].ExpectedGain = score.ExpectedGain
		r.escalations[i].Duplicate = score.Duplicate
		return
	}
}

func (r *Runtime) newIDLocked(prefix string) string {
	r.nextID++
	return fmt.Sprintf("%s-%d", prefix, r.nextID)
}

// --- Observer ---------------------------------------------------------

// CycleStarting is the loop's safe boundary, and therefore where pause is
// enforced. Nothing is in flight here: no model process is running, no
// quota is committed, and memory is consistent — which is exactly why a
// pause waits for this point instead of killing work already paid for.
func (r *Runtime) CycleStarting(ctx context.Context, project string, cycle int) error {
	for {
		r.mu.Lock()
		r.cycle = cycle
		if !r.pauseRequested {
			if r.pausedAnnounced {
				r.pausedAnnounced = false
				r.state = control.RunStateRunning
				r.mu.Unlock()
				r.publish(invocation.EventResearchResumed, map[string]any{"project": project, "cycle": cycle})
				continue
			}
			r.state = control.RunStateRunning
			r.mu.Unlock()
			return nil
		}

		announce := !r.pausedAnnounced
		r.pausedAnnounced = true
		r.state = control.RunStatePaused
		wait := r.resumeCh
		r.mu.Unlock()

		if announce {
			r.publish(invocation.EventResearchPaused, map[string]any{"project": project, "cycle": cycle})
		}

		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *Runtime) AgentStarted(project, agent, task string) {
	r.mu.Lock()
	snap := r.agentSnapshotLocked(agent)
	snap.State = control.AgentWorking
	snap.CurrentTask = task
	snap.StartedAt = time.Now()
	r.agents[agent] = snap
	r.activeAgent = agent
	r.mu.Unlock()

	// Narrating the lifecycle into the agent's own log stream means a log
	// pane still reads coherently for a provider that prints nothing.
	r.publishLog(agent, control.StreamSystem, "invocation started: "+task)
}

func (r *Runtime) AgentFinished(project, agent string, envelope invocation.Envelope, err error) {
	r.mu.Lock()
	snap := r.agentSnapshotLocked(agent)
	snap.CurrentTask = ""
	snap.StartedAt = time.Time{}
	switch {
	case err != nil:
		snap.State = control.AgentFailed
		snap.LastSummary = err.Error()
		r.lastError = err.Error()
	case envelope.Status == invocation.StatusBlocked:
		snap.State = control.AgentBlocked
		snap.LastSummary = envelope.Summary
	default:
		snap.State = control.AgentIdle
		if envelope.Summary != "" {
			snap.LastSummary = envelope.Summary
			r.latest = envelope.Summary
		}
	}
	if envelope.Frontier != "" {
		r.frontier = envelope.Frontier
	}
	r.agents[agent] = snap
	if r.activeAgent == agent {
		r.activeAgent = ""
	}
	r.mu.Unlock()

	if err != nil {
		r.publishLog(agent, control.StreamSystem, "invocation failed: "+err.Error())
		return
	}
	r.publishLog(agent, control.StreamSystem, "invocation completed: "+string(envelope.Status))
}

func (r *Runtime) agentSnapshotLocked(agent string) control.AgentSnapshot {
	if snap, ok := r.agents[agent]; ok {
		return snap
	}
	snap := control.AgentSnapshot{Name: agent, State: control.AgentIdle}
	r.agentOrder = append(r.agentOrder, agent)
	return snap
}

// --- SteeringSource ---------------------------------------------------

// Pending returns undelivered steering instructions from shared memory.
func (r *Runtime) Pending(ctx context.Context, project string) ([]SteeringMessage, error) {
	recs, err := r.store.Retrieve(ctx, memory.RetrieveInput{
		Project: project,
		Kinds:   []memory.Kind{memory.KindSteering},
		States:  []string{memory.StateObserved},
		TopK:    50,
	})
	if err != nil {
		return nil, fmt.Errorf("research: pending steering: %w", err)
	}

	out := make([]SteeringMessage, 0, len(recs))
	for _, rec := range recs {
		if rec.State != memory.StateObserved {
			continue
		}
		out = append(out, SteeringMessage{ID: rec.ID, Message: rec.Summary})
	}
	return out, nil
}

// MarkApplied moves delivered instructions out of the pending state.
func (r *Runtime) MarkApplied(ctx context.Context, project string, ids []string, cycle int) error {
	for _, id := range ids {
		reason := fmt.Sprintf("applied in cycle %d", cycle)
		if _, err := r.store.Accept(ctx, project, id, reason, "runtime"); err != nil {
			return fmt.Errorf("research: mark steering applied: %w", err)
		}
	}
	return nil
}

// --- Approver ---------------------------------------------------------

// RequestApproval blocks the scheduler until a human approves or denies,
// which is the whole point of manual mode: the scarce agent must not run
// until someone has said yes.
//
// The runtime only relays the answer. It never invokes Astra itself — the
// scheduler and loop keep sole responsibility for execution, so no
// interface can spend scarce quota by a path the CLI does not have.
func (r *Runtime) RequestApproval(ctx context.Context, req scheduler.DecisionRequest) (bool, string, error) {
	r.mu.Lock()
	summary := r.lastScore
	summary.ID = r.newIDLocked("pending")
	summary.RequestedAt = time.Now()
	summary.RequestingAgent = AgentAri
	summary.TargetAgent = AgentAstra
	summary.Question = req.Context
	summary.Decision = control.DecisionPending
	summary.Budget = r.qacSnapshotLocked()

	p := &pendingEscalation{summary: summary, decided: make(chan approvalDecision, 1)}
	r.pending = p

	if snap, ok := r.agents[AgentAri]; ok {
		snap.State = control.AgentWaiting
		r.agents[AgentAri] = snap
	}
	r.mu.Unlock()

	r.publish(invocation.EventEscalationRequested, map[string]any{
		"project": r.project, "question": req.Context,
		"escalation_id": summary.ID, "awaiting_approval": true,
	})

	select {
	case decision := <-p.decided:
		r.clearPending()
		return decision.approved, decision.reason, nil
	case <-ctx.Done():
		r.clearPending()
		return false, "approval cancelled", ctx.Err()
	}
}

func (r *Runtime) clearPending() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending = nil
	if snap, ok := r.agents[AgentAri]; ok && snap.State == control.AgentWaiting {
		snap.State = control.AgentWorking
		r.agents[AgentAri] = snap
	}
}

// failPendingApproval releases a blocked approval as a denial, so a
// shutting-down run never leaves the scheduler waiting on a human who can
// no longer answer.
func (r *Runtime) failPendingApproval(reason string) {
	r.mu.Lock()
	p := r.pending
	r.pending = nil
	r.mu.Unlock()

	if p != nil {
		select {
		case p.decided <- approvalDecision{approved: false, reason: reason}:
		default:
		}
	}
}

// --- publishing -------------------------------------------------------

func (r *Runtime) publish(event string, fields map[string]any) {
	if r.loop != nil && r.loop.Events != nil {
		// Route through the loop's sink so the event reaches the durable
		// JSONL log as well as live subscribers, exactly like every event
		// the loop itself emits.
		r.loop.Events.Log(event, fields)
		return
	}
	if r.bus != nil {
		r.bus.Log(event, fields)
	}
}

func (r *Runtime) publishLog(agent, stream, text string) {
	if r.logs == nil {
		return
	}
	r.logs.Publish(control.AgentLogEntry{
		Project: r.project, Agent: agent, Stream: stream,
		Time: time.Now().UTC(), Text: text,
	})
}

// AgentWriter returns a writer that publishes an agent's output into the
// runtime's log bus, tagged with this project.
func (r *Runtime) AgentWriter(agent, stream string) *control.LogWriter {
	return r.logs.Writer(r.project, agent, stream)
}

// SetDecider attaches the scheduler after construction. The wiring is
// circular by nature under manual QAC — the scheduler needs the runtime as
// its Approver, and the runtime needs the scheduler for budget state — so
// one side is necessarily assigned second.
func (r *Runtime) SetDecider(decider Decider) {
	if r.loop != nil {
		r.loop.Decider = decider
	}
	if reporter, ok := decider.(budgetReporter); ok {
		r.budget = reporter
	}
}

func numberOr(ev control.Event, key string, fallback float64) float64 {
	if v, ok := ev.Number(key); ok {
		return v
	}
	return fallback
}

func appendBoundedSlice[T any](buf []T, item T, max int) []T {
	buf = append(buf, item)
	if len(buf) > max {
		buf = buf[len(buf)-max:]
	}
	return buf
}
