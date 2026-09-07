// Package research implements the Ari -> QAC -> Astra -> Ari research
// workflow cycle. It has no dependency on internal/worksource: research
// projects aren't issues, PRs, or GitHub/Linear labels, and are wired
// directly from the CLI (see cmd/ariadne) rather than through a
// TaskPoller/Router.
package research

import (
	"context"
	"fmt"

	"github.com/haha-systems/ariadne/internal/agentprofile"
	"github.com/haha-systems/ariadne/internal/invocation"
	"github.com/haha-systems/ariadne/internal/memory"
	"github.com/haha-systems/ariadne/internal/research/prompts"
	"github.com/haha-systems/ariadne/internal/scheduler"
)

// Agent identities the loop records in events and memory. These are
// workflow roles, not profile names: the configured profile behind "ari"
// can change without changing who the loop thinks is doing routine work.
const (
	AgentAri   = "ari"
	AgentAstra = "astra"
)

// Decider is the narrow slice of *scheduler.Scheduler the loop depends on,
// defined locally so tests can inject a scripted fake instead of
// constructing a real Scheduler/Budget/Approver.
type Decider interface {
	Decide(ctx context.Context, req scheduler.DecisionRequest) (scheduler.Decision, error)
}

// AriLoop runs the routine-agent/scarce-agent research cycle described in
// the spec: Ari does one bounded routine step per invocation; when it
// requests escalation, Decider (backed by QAC) weighs the request's
// epistemic value and decides whether Astra gets involved; control always
// returns to Ari afterward, never to Astra recursively.
type AriLoop struct {
	Ari, Astra invocation.Runner
	Decider    Decider
	Memory     memory.Store
	// Events is an EventSink rather than a concrete writer so the same
	// event can reach the durable JSONL log and any live subscribers at
	// once (see invocation.MultiSink). Nil disables event logging.
	Events invocation.EventSink

	// Observer, when set, watches the loop and gates it at cycle
	// boundaries. Steering, when set, supplies pending user instructions.
	// Both are optional: with neither, the loop behaves exactly as the CLI
	// has always run it.
	Observer Observer
	Steering SteeringSource

	// AriProfileName/AstraProfileName identify the profiles offered to
	// Decider — used only for scheduling, never surfaced to prompts.
	AriProfileName   string
	AstraProfileName string

	// AriModel/AstraModel are recorded in events for auditability only.
	// The loop never reasons about them: which model backs a profile is a
	// configuration fact, and keeping the decision logic in terms of
	// CostClass/ScarcityClass is what lets a profile be re-pointed at a
	// different model without touching the workflow (see package
	// agentprofile). They are labels in the log, nothing more.
	AriModel   string
	AstraModel string

	// Guidance is durable, project-specific instruction text (e.g. the
	// research team's overall mandate) fenced into every Ari and Astra
	// prompt as reference material. Loaded once by the caller (see
	// cmd/ariadne's guidance-file convention) rather than re-read every
	// cycle.
	//
	// It is deliberately never the source of agent identity: guidance
	// describing Astra as the lead researcher must not turn an Ari
	// invocation into Astra. See package research/prompts.
	Guidance string

	// StrictPersistence makes a failed write of research state fatal to
	// the run. Research state is not best-effort: a cycle whose frontier
	// silently failed to persist leaves the next cycle re-deriving
	// context it already paid for. Callers using a durable backend should
	// set this; callers who explicitly opted into an in-memory fallback
	// should not.
	StrictPersistence bool

	// pendingFeedback carries a denied escalation's explanation into the
	// next cycle's prompt, so Ari learns why the request failed instead of
	// re-sending it.
	pendingFeedback string

	// deniedFingerprints records escalation requests already denied this
	// run, for duplicate detection. Attaching new evidence changes a
	// request's fingerprint, which is the intended escape hatch.
	deniedFingerprints map[string]bool
}

// Run executes up to maxCycles routine iterations for project, stopping
// early on StatusComplete/StatusBlocked or an invocation error.
//
// Memory is validated before the first invocation: an unusable persistence
// backend fails here, cheaply, rather than after a cycle of real research
// whose results cannot be saved.
func (l *AriLoop) Run(ctx context.Context, project string, maxCycles int) error {
	if err := memory.Preflight(ctx, l.Memory, project); err != nil {
		l.logEvent(invocation.EventMemoryPreflightFailed, map[string]any{"project": project, "error": err.Error()})
		return fmt.Errorf("research: memory preflight for project %q: %w", project, err)
	}
	l.logEvent(invocation.EventMemoryPreflightPassed, map[string]any{"project": project})

	for cycle := 0; cycle < maxCycles; cycle++ {
		done, err := l.runCycle(ctx, project, cycle)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	return nil
}

func (l *AriLoop) runCycle(ctx context.Context, project string, cycle int) (done bool, err error) {
	// The gate comes first, before any work: this is the only point in a
	// cycle where nothing is in flight, so it is the only safe place to
	// hold the loop without discarding an invocation that has already cost
	// real quota.
	if l.Observer != nil {
		if err := l.Observer.CycleStarting(ctx, project, cycle); err != nil {
			return true, err
		}
	}

	l.logEvent(invocation.EventResearchCycleStarted, map[string]any{"project": project, "cycle": cycle})

	steering := l.pendingSteering(ctx, project)
	prompt := l.buildAriPrompt(ctx, project, cycle, steeringTexts(steering))

	// Recorded as its own event so a run log proves which identity each
	// invocation was given — the identity-leakage failure was invisible in
	// the old logs, which only showed that "ari" had been invoked.
	l.logEvent(invocation.EventAriIdentity, map[string]any{
		"project": project, "cycle": cycle,
		"agent": AgentAri, "profile": l.AriProfileName, "not": AgentAstra,
	})

	l.logEvent(invocation.EventAriInvocationStarted, map[string]any{
		"agent": AgentAri, "project": project, "cycle": cycle,
		"profile": l.AriProfileName, "model": l.AriModel,
	})
	l.notifyAgentStarted(project, AgentAri, "routine research step")
	envelope, err := l.Ari.Invoke(ctx, invocation.Request{Prompt: prompt, Depth: 0})
	if err != nil {
		l.notifyAgentFinished(project, AgentAri, invocation.Envelope{}, err)
		l.logEvent(invocation.EventInvocationFailed, map[string]any{"agent": AgentAri, "project": project, "cycle": cycle, "error": err.Error()})
		return false, fmt.Errorf("research: ari invocation: %w", err)
	}
	l.notifyAgentFinished(project, AgentAri, envelope, nil)

	// Steering is marked applied only now, once it has actually reached an
	// agent — see SteeringSource.
	l.markSteeringApplied(ctx, project, steering, cycle)
	l.logEvent(invocation.EventAriInvocationCompleted, map[string]any{
		"agent": AgentAri, "project": project, "cycle": cycle,
		"profile": l.AriProfileName, "model": l.AriModel,
		"status": string(envelope.Status), "work_completed": envelope.WorkCompleted,
	})

	if err := l.persistEnvelope(ctx, project, envelope, AgentAri); err != nil {
		return false, err
	}

	switch envelope.Status {
	case invocation.StatusComplete, invocation.StatusBlocked:
		return true, nil

	case invocation.StatusEscalate:
		if err := l.handleEscalation(ctx, project, cycle, envelope); err != nil {
			return false, err
		}
		l.logEvent(invocation.EventControlReturnedToAri, map[string]any{"project": project, "cycle": cycle})
		return false, nil

	default: // StatusContinue and anything unrecognized
		return false, nil
	}
}

// handleEscalation scores Ari's request, asks Decider whether to spend
// Astra on it, and either invokes Astra or records actionable feedback for
// the next cycle.
//
// Failures obtaining a decision or invoking Astra are recorded as events
// and swallowed — research cycles should keep running productively even
// when a single escalation attempt fails, per the scheduler-feedback design
// (spec §43). A persistence failure under StrictPersistence is the one
// exception, and is returned.
func (l *AriLoop) handleEscalation(ctx context.Context, project string, cycle int, envelope invocation.Envelope) error {
	req := envelope.EscalationRequest()
	fingerprint := escalationFingerprint(req)
	duplicate := l.deniedFingerprints[fingerprint]
	score := ScoreEscalation(req, duplicate)

	l.logEvent(invocation.EventEscalationRequested, map[string]any{
		"project": project, "cycle": cycle,
		"question": req.Question, "reason": req.Reason,
		"failed_local_attempts":  req.FailedLocalAttempts,
		"cheaper_test_available": req.CheaperTestAvailable,
		"fingerprint":            fingerprint,
	})

	scoreFields := score.Fields()
	scoreFields["project"] = project
	scoreFields["cycle"] = cycle
	scoreFields["continue_weight"] = continueWeight
	l.logEvent(invocation.EventEscalationScored, scoreFields)

	escalationRec, err := l.observe(ctx, project, memory.KindEscalation, AgentAri, req.Question, AgentAri, req.EvidenceRefs)
	if err != nil {
		return err
	}

	// A materially identical request that was already denied never
	// reaches the scheduler: re-deciding it would spend budget on a
	// question the scheduler has already answered.
	if duplicate {
		reason := "duplicate of a previously denied escalation request with no new evidence"
		l.logEvent(invocation.EventEscalationDenied, map[string]any{
			"project": project, "cycle": cycle, "reason": reason, "duplicate_of_denied": true,
		})
		l.pendingFeedback = denialFeedback(req, reason, score)
		if escalationRec != nil {
			return l.reject(ctx, project, escalationRec.ID, "denied: "+reason, "scheduler")
		}
		return nil
	}

	decision, err := l.Decider.Decide(ctx, scheduler.DecisionRequest{
		Context:      envelope.EscalationContext(),
		EvidenceRefs: req.EvidenceRefs,
		Depth:        0,
		Options: []scheduler.Action{
			{Name: "continue", ProfileName: l.AriProfileName, ScarcityClass: agentprofile.ScarcityPlentiful, Weight: continueWeight},
			{Name: "escalate", ProfileName: l.AstraProfileName, ScarcityClass: agentprofile.ScarcityRare, Weight: score.Total},
		},
	})
	if err != nil {
		l.logEvent(invocation.EventInvocationFailed, map[string]any{"project": project, "cycle": cycle, "stage": "escalation_decision", "error": err.Error()})
		return nil
	}

	if !decision.Escalated {
		// The scheduler reports the request's own Context as its reason
		// when it simply preferred the better-weighted routine option, which
		// would echo Ari's question back at it as an "explanation". Say what
		// actually happened instead: the request lost on epistemic value.
		reason := decision.Reason
		if score.Total < continueWeight {
			reason = fmt.Sprintf(
				"escalation scored %.2f, below the %.2f continue baseline — routine work remains the better use of this cycle",
				score.Total, continueWeight)
		}

		l.logEvent(invocation.EventEscalationDenied, l.withBudget(map[string]any{
			"project": project, "cycle": cycle, "reason": reason, "score": score.Total,
		}))
		l.markDenied(fingerprint)
		l.pendingFeedback = denialFeedback(req, reason, score)
		if escalationRec != nil {
			return l.reject(ctx, project, escalationRec.ID, "denied: "+reason, "scheduler")
		}
		return nil
	}

	if escalationRec != nil {
		if err := l.accept(ctx, project, escalationRec.ID, "approved: "+decision.Reason, "scheduler"); err != nil {
			return err
		}
	}

	l.logEvent(invocation.EventEscalationApproved, l.withBudget(map[string]any{
		"project": project, "cycle": cycle, "reason": decision.Reason, "score": score.Total,
	}))
	return l.invokeAstra(ctx, project, cycle, req, decision)
}

// invokeAstra builds a compact packet, invokes Astra, and persists its
// output. If Astra's own envelope requests further escalation, that request
// is fed back through Decider at Depth 1 purely to exercise (and rely on)
// the recursion guard — the outcome is always to log-and-continue, never to
// invoke a scarce agent recursively. Control returns to Ari either way.
func (l *AriLoop) invokeAstra(ctx context.Context, project string, cycle int, req invocation.Escalation, decision scheduler.Decision) error {
	packet, err := BuildPacket(ctx, l.Memory, project, req, l.Guidance, decision.Reason)
	if err != nil {
		l.logEvent(invocation.EventInvocationFailed, map[string]any{"project": project, "cycle": cycle, "stage": "build_packet", "error": err.Error()})
		return nil
	}

	l.logEvent(invocation.EventAstraInvocationStarted, map[string]any{
		"agent": AgentAstra, "project": project, "cycle": cycle,
		"profile": l.AstraProfileName, "model": l.AstraModel,
	})
	l.notifyAgentStarted(project, AgentAstra, req.Question)
	astraEnvelope, err := l.Astra.Invoke(ctx, invocation.Request{Prompt: packet.Render(), Depth: 1})
	if err != nil {
		l.notifyAgentFinished(project, AgentAstra, invocation.Envelope{}, err)
		l.logEvent(invocation.EventInvocationFailed, map[string]any{"agent": AgentAstra, "project": project, "cycle": cycle, "error": err.Error()})
		return nil
	}
	l.notifyAgentFinished(project, AgentAstra, astraEnvelope, nil)
	l.logEvent(invocation.EventAstraInvocationCompleted, map[string]any{
		"agent": AgentAstra, "project": project, "cycle": cycle,
		"profile": l.AstraProfileName, "model": l.AstraModel,
		"status": string(astraEnvelope.Status),
	})

	// Astra's summary is its intervention, so it is recorded as a Decision
	// rather than an observation — that is what LoadContext reads back as
	// LAST ASTRA DECISION for Ari's next cycle.
	if err := l.persistEnvelope(ctx, project, astraEnvelope, AgentAstra); err != nil {
		return err
	}

	if astraEnvelope.Status == invocation.StatusEscalate {
		_, err := l.Decider.Decide(ctx, scheduler.DecisionRequest{
			Context: astraEnvelope.EscalationContext(),
			Depth:   1,
			Options: []scheduler.Action{
				{Name: "escalate again", ProfileName: l.AstraProfileName, ScarcityClass: agentprofile.ScarcityRare, Weight: 1},
			},
		})
		// This must always fail — a scarce agent must not recursively
		// invoke itself. Log it and return control to Ari regardless.
		l.logEvent(invocation.EventInvocationFailed, map[string]any{
			"project": project, "cycle": cycle, "stage": "astra_recursion_rejected",
			"detail": errString(err),
		})
	}
	return nil
}

// buildAriPrompt assembles Ari's prompt from its fixed identity, the
// project guidance, and the durable research context recovered from memory
// — not from a single frontier summary, so a cycle does not have to
// re-derive what previous cycles already established.
func (l *AriLoop) buildAriPrompt(ctx context.Context, project string, cycle int, steering []string) string {
	rc := LoadContext(ctx, l.Memory, project)
	l.logEvent(invocation.EventFrontierLoaded, map[string]any{
		"project": project, "cycle": cycle,
		"frontier_present":    !rc.FrontierMissing,
		"observations":        len(rc.Observations),
		"active_hypotheses":   len(rc.ActiveHypotheses),
		"rejected_hypotheses": len(rc.RejectedHypotheses),
		"open_questions":      len(rc.OpenQuestions),
	})

	feedback := l.pendingFeedback
	l.pendingFeedback = "" // delivered exactly once

	return prompts.BuildAri(prompts.AriInput{
		Project:        project,
		Guidance:       l.Guidance,
		Context:        rc.Render(),
		DenialFeedback: feedback,
		Steering:       steering,
	})
}

// pendingSteering fetches undelivered user instructions. A failure to read
// them is logged and tolerated: losing a steering message is bad, but
// halting an otherwise-healthy research programme over it is worse.
func (l *AriLoop) pendingSteering(ctx context.Context, project string) []SteeringMessage {
	if l.Steering == nil {
		return nil
	}
	msgs, err := l.Steering.Pending(ctx, project)
	if err != nil {
		l.logEvent(invocation.EventInvocationFailed, map[string]any{
			"project": project, "stage": "steering_pending", "error": err.Error(),
		})
		return nil
	}
	for _, m := range msgs {
		l.logEvent(invocation.EventSteeringReceived, map[string]any{
			"project": project, "steering_id": m.ID, "message": m.Message,
		})
	}
	return msgs
}

func (l *AriLoop) markSteeringApplied(ctx context.Context, project string, msgs []SteeringMessage, cycle int) {
	if l.Steering == nil || len(msgs) == 0 {
		return
	}
	ids := steeringIDs(msgs)
	if err := l.Steering.MarkApplied(ctx, project, ids, cycle); err != nil {
		l.logEvent(invocation.EventInvocationFailed, map[string]any{
			"project": project, "stage": "steering_apply", "error": err.Error(),
		})
		return
	}
	for _, id := range ids {
		l.logEvent(invocation.EventSteeringApplied, map[string]any{
			"project": project, "steering_id": id, "cycle": cycle,
		})
	}
}

func (l *AriLoop) notifyAgentStarted(project, agent, task string) {
	if l.Observer != nil {
		l.Observer.AgentStarted(project, agent, task)
	}
}

func (l *AriLoop) notifyAgentFinished(project, agent string, envelope invocation.Envelope, err error) {
	if l.Observer != nil {
		l.Observer.AgentFinished(project, agent, envelope, err)
	}
}

// persistEnvelope writes a completed invocation's structured result to
// durable memory before the next cycle begins. Each field lands under its
// own Subject (see context.go) so the next cycle's LoadContext can rebuild
// the sections separately rather than re-parsing one summary blob.
func (l *AriLoop) persistEnvelope(ctx context.Context, project string, envelope invocation.Envelope, agentID string) error {
	if envelope.Summary != "" {
		// A scarce agent's summary is its intervention — a decision the
		// programme is expected to act on — while a routine agent's is one
		// more observation. Recording them under different kinds is what
		// lets the next cycle surface Astra's ruling specifically.
		kind, subject := memory.KindObservation, SubjectObservation
		if agentID == AgentAstra {
			kind, subject = memory.KindDecision, AgentAstra
		}
		if _, err := l.observe(ctx, project, kind, subject, envelope.Summary, agentID, nil); err != nil {
			return err
		}
	}
	for _, obs := range envelope.Observations {
		if _, err := l.observe(ctx, project, memory.KindObservation, SubjectObservation, obs, agentID, nil); err != nil {
			return err
		}
	}
	for _, q := range envelope.OpenQuestions {
		if _, err := l.observe(ctx, project, memory.KindObservation, SubjectOpenQuestion, q, agentID, nil); err != nil {
			return err
		}
	}
	for _, c := range envelope.Confounds {
		if _, err := l.observe(ctx, project, memory.KindObservation, SubjectConfound, c, agentID, nil); err != nil {
			return err
		}
	}
	if envelope.WorkCompleted != "" {
		if _, err := l.observe(ctx, project, memory.KindObservation, SubjectWorkCompleted, envelope.WorkCompleted, agentID, nil); err != nil {
			return err
		}
	}
	if envelope.NextCandidateStep != "" {
		if _, err := l.observe(ctx, project, memory.KindObservation, SubjectExperiment, envelope.NextCandidateStep, agentID, nil); err != nil {
			return err
		}
	}
	for _, h := range envelope.Hypotheses {
		if _, err := l.observe(ctx, project, memory.KindHypothesis, SubjectHypothesis, h, agentID, nil); err != nil {
			return err
		}
	}
	// A rejected hypothesis is recorded and then transitioned, rather than
	// simply dropped: "we tried this and it failed" is the single most
	// reusable thing a research cycle produces, and the next cycle needs to
	// see it to avoid repeating the work.
	for _, h := range envelope.RejectedHypotheses {
		rec, err := l.observe(ctx, project, memory.KindHypothesis, SubjectHypothesis, h, agentID, nil)
		if err != nil {
			return err
		}
		if rec == nil {
			continue
		}
		if err := l.reject(ctx, project, rec.ID, "reported as rejected by "+agentID, agentID); err != nil {
			return err
		}
	}

	return l.applyFrontierUpdate(ctx, project, envelope, agentID)
}

// applyFrontierUpdate persists envelope.Frontier, if the agent set one, so
// progress recorded in a structured result actually becomes the durable
// frontier rather than only living in this invocation's Summary.
func (l *AriLoop) applyFrontierUpdate(ctx context.Context, project string, envelope invocation.Envelope, agentID string) error {
	if envelope.Frontier == "" {
		return nil
	}
	if _, err := l.Memory.WriteFrontier(ctx, project, envelope.Frontier, agentID); err != nil {
		return l.memoryFailure(project, "memory_write_frontier", err)
	}
	l.logEvent(invocation.EventMemoryWritten, map[string]any{"project": project, "stage": "memory_write_frontier", "agent_id": agentID})
	return nil
}

// observe writes a Record and returns it. A write failure is reported
// through memoryFailure: fatal under StrictPersistence, otherwise logged
// and tolerated with a nil Record.
func (l *AriLoop) observe(ctx context.Context, project string, kind memory.Kind, subject, summary, agentID string, evidenceRefs []string) (*memory.Record, error) {
	rec, err := l.Memory.Observe(ctx, memory.ObserveInput{
		Project: project, Subject: subject, Summary: summary, Kind: kind, AgentID: agentID, EvidenceRefs: evidenceRefs,
	})
	if err != nil {
		return nil, l.memoryFailure(project, "memory_observe", err)
	}
	l.logEvent(invocation.EventMemoryWritten, map[string]any{"project": project, "kind": string(kind), "subject": subject, "agent_id": agentID})
	return &rec, nil
}

func (l *AriLoop) accept(ctx context.Context, project, recordID, reason, agentID string) error {
	if _, err := l.Memory.Accept(ctx, project, recordID, reason, agentID); err != nil {
		return l.memoryFailure(project, "memory_accept", err)
	}
	l.logEvent(invocation.EventMemoryWritten, map[string]any{"project": project, "stage": "memory_accept", "record_id": recordID})
	return nil
}

func (l *AriLoop) reject(ctx context.Context, project, recordID, reason, agentID string) error {
	if _, err := l.Memory.Reject(ctx, project, recordID, reason, agentID); err != nil {
		return l.memoryFailure(project, "memory_reject", err)
	}
	l.logEvent(invocation.EventMemoryWritten, map[string]any{"project": project, "stage": "memory_reject", "record_id": recordID})
	return nil
}

// memoryFailure records a failed write and decides whether it ends the run.
// A silently-swallowed write failure is what let a whole research pass run
// against a 401 and persist nothing, so the failure is always logged as
// memory_write_failed and, under StrictPersistence, stops the workflow
// rather than letting it continue as though state had been saved.
func (l *AriLoop) memoryFailure(project, stage string, err error) error {
	l.logEvent(invocation.EventMemoryWriteFailed, map[string]any{
		"project": project, "stage": stage, "error": err.Error(), "strict": l.StrictPersistence,
	})
	if l.StrictPersistence {
		return fmt.Errorf("research: %s for project %q: %w", stage, project, err)
	}
	return nil
}

// budgetReporter is the optional slice of a Decider that can describe its
// own quota state, defined here (per the repo's consuming-package
// convention) so the loop can annotate escalation decisions with the budget
// that produced them without requiring every Decider to supply one.
type budgetReporter interface {
	BudgetSnapshot() scheduler.BudgetSnapshot
}

// withBudget annotates an escalation-decision event with the scheduler's
// quota state. A denial reading "budget exhausted" is only actionable
// alongside the counts and cooldown that exhausted it.
func (l *AriLoop) withBudget(fields map[string]any) map[string]any {
	reporter, ok := l.Decider.(budgetReporter)
	if !ok {
		return fields
	}
	snap := reporter.BudgetSnapshot()
	fields["budget_cycle_count"] = snap.CycleCount
	fields["budget_per_cycle_max"] = snap.PerCycleMax
	fields["budget_session_count"] = snap.SessionCount
	fields["budget_per_session_max"] = snap.PerSessionMax
	fields["budget_cooldown_seconds"] = snap.Cooldown.Seconds()
	return fields
}

func (l *AriLoop) markDenied(fingerprint string) {
	if l.deniedFingerprints == nil {
		l.deniedFingerprints = make(map[string]bool)
	}
	l.deniedFingerprints[fingerprint] = true
}

func (l *AriLoop) logEvent(event string, fields map[string]any) {
	if l.Events == nil {
		return
	}
	l.Events.Log(event, fields)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
