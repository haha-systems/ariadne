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
	"github.com/haha-systems/ariadne/internal/scheduler"
)

// Decider is the narrow slice of *scheduler.Scheduler the loop depends on,
// defined locally so tests can inject a scripted fake instead of
// constructing a real Scheduler/Budget/Approver.
type Decider interface {
	Decide(ctx context.Context, req scheduler.DecisionRequest) (scheduler.Decision, error)
}

// AriLoop runs the routine-agent/scarce-agent research cycle described in
// the spec: Ari does routine work; when it requests escalation, Decider
// (backed by QAC) decides whether Astra gets involved; control always
// returns to Ari afterward, never to Astra recursively.
type AriLoop struct {
	Ari, Astra invocation.Runner
	Decider    Decider
	Memory     memory.Store
	Events     *invocation.EventWriter

	// AriProfileName/AstraProfileName identify the profiles offered to
	// Decider — used only for scheduling, never surfaced to prompts.
	AriProfileName   string
	AstraProfileName string
}

// Run executes up to maxCycles routine iterations for project, stopping
// early on StatusComplete/StatusBlocked or an invocation error.
func (l *AriLoop) Run(ctx context.Context, project string, maxCycles int) error {
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
	prompt := l.buildAriPrompt(ctx, project)

	l.logEvent(invocation.EventInvocationStarted, map[string]any{"agent": "ari", "project": project, "cycle": cycle})
	envelope, err := l.Ari.Invoke(ctx, invocation.Request{Prompt: prompt, Depth: 0})
	if err != nil {
		l.logEvent(invocation.EventInvocationFailed, map[string]any{"agent": "ari", "project": project, "cycle": cycle, "error": err.Error()})
		return false, fmt.Errorf("research: ari invocation: %w", err)
	}
	l.logEvent(invocation.EventInvocationCompleted, map[string]any{"agent": "ari", "project": project, "cycle": cycle, "status": string(envelope.Status)})

	if envelope.Summary != "" {
		l.observe(ctx, project, memory.KindObservation, "ari", envelope.Summary, "ari", nil)
	}

	switch envelope.Status {
	case invocation.StatusComplete, invocation.StatusBlocked:
		return true, nil

	case invocation.StatusEscalate:
		l.handleEscalation(ctx, project, cycle, envelope)
		return false, nil

	default: // StatusContinue and anything unrecognized
		return false, nil
	}
}

// handleEscalation asks Decider whether to invoke Astra. Failures obtaining
// a decision or invoking Astra are recorded as events and swallowed —
// research cycles should keep running productively even when a single
// escalation attempt fails, per the scheduler-feedback design (spec §43).
func (l *AriLoop) handleEscalation(ctx context.Context, project string, cycle int, envelope invocation.Envelope) {
	req := scheduler.DecisionRequest{
		Context:      envelope.EscalationReason,
		EvidenceRefs: nil,
		Depth:        0,
		Options: []scheduler.Action{
			{Name: "continue", ProfileName: l.AriProfileName, ScarcityClass: agentprofile.ScarcityPlentiful, Weight: 0.4},
			{Name: "escalate", ProfileName: l.AstraProfileName, ScarcityClass: agentprofile.ScarcityRare, Weight: 0.6},
		},
	}
	l.logEvent(invocation.EventEscalationRequested, map[string]any{"project": project, "cycle": cycle, "reason": envelope.EscalationReason})
	escalationRec := l.observe(ctx, project, memory.KindEscalation, "ari", envelope.EscalationReason, "ari", nil)

	decision, err := l.Decider.Decide(ctx, req)
	if err != nil {
		l.logEvent(invocation.EventInvocationFailed, map[string]any{"project": project, "cycle": cycle, "stage": "escalation_decision", "error": err.Error()})
		return
	}

	if !decision.Escalated {
		l.logEvent(invocation.EventEscalationDenied, map[string]any{"project": project, "cycle": cycle, "reason": decision.Reason})
		if escalationRec != nil {
			l.reject(ctx, project, escalationRec.ID, "denied: "+decision.Reason, "scheduler")
		}
		return
	}

	if escalationRec != nil {
		l.accept(ctx, project, escalationRec.ID, "approved: "+decision.Reason, "scheduler")
	}

	l.logEvent(invocation.EventEscalationApproved, map[string]any{"project": project, "cycle": cycle, "reason": decision.Reason})
	l.invokeAstra(ctx, project, cycle, envelope)
}

// invokeAstra builds a compact packet, invokes Astra, and persists its
// output. If Astra's own envelope requests further escalation, that request
// is fed back through Decider at Depth 1 purely to exercise (and rely on)
// the recursion guard — the outcome is always to log-and-continue, never to
// invoke a scarce agent recursively. Control returns to Ari either way.
func (l *AriLoop) invokeAstra(ctx context.Context, project string, cycle int, ariEnvelope invocation.Envelope) {
	packet, err := BuildPacket(ctx, l.Memory, project, ariEnvelope.EscalationReason, nil)
	if err != nil {
		l.logEvent(invocation.EventInvocationFailed, map[string]any{"project": project, "cycle": cycle, "stage": "build_packet", "error": err.Error()})
		return
	}

	l.logEvent(invocation.EventInvocationStarted, map[string]any{"agent": "astra", "project": project, "cycle": cycle})
	astraEnvelope, err := l.Astra.Invoke(ctx, invocation.Request{Prompt: packet.Render(), Depth: 1})
	if err != nil {
		l.logEvent(invocation.EventInvocationFailed, map[string]any{"agent": "astra", "project": project, "cycle": cycle, "error": err.Error()})
		return
	}
	l.logEvent(invocation.EventInvocationCompleted, map[string]any{"agent": "astra", "project": project, "cycle": cycle, "status": string(astraEnvelope.Status)})

	if astraEnvelope.Summary != "" {
		l.observe(ctx, project, memory.KindDecision, "astra", astraEnvelope.Summary, "astra", nil)
	}

	if astraEnvelope.Status == invocation.StatusEscalate {
		_, err := l.Decider.Decide(ctx, scheduler.DecisionRequest{
			Context: astraEnvelope.EscalationReason,
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
}

func (l *AriLoop) buildAriPrompt(ctx context.Context, project string) string {
	if frontier, err := l.Memory.Frontier(ctx, project); err == nil && frontier.Summary != "" {
		return fmt.Sprintf("Project: %s\n\nCurrent frontier:\n\n%s\n\nContinue routine research work.", project, frontier.Summary)
	}
	return fmt.Sprintf("Project: %s\n\nNo frontier recorded yet. Begin routine research work.", project)
}

// observe writes a Record and returns it, or nil if the write failed (the
// failure is logged as an event rather than propagated, since a memory
// write failure shouldn't halt an otherwise-productive research cycle).
func (l *AriLoop) observe(ctx context.Context, project string, kind memory.Kind, subject, summary, agentID string, evidenceRefs []string) *memory.Record {
	rec, err := l.Memory.Observe(ctx, memory.ObserveInput{
		Project: project, Subject: subject, Summary: summary, Kind: kind, AgentID: agentID, EvidenceRefs: evidenceRefs,
	})
	if err != nil {
		l.logEvent(invocation.EventInvocationFailed, map[string]any{"project": project, "stage": "memory_observe", "error": err.Error()})
		return nil
	}
	l.logEvent(invocation.EventMemoryWritten, map[string]any{"project": project, "kind": string(kind), "agent_id": agentID})
	return &rec
}

func (l *AriLoop) accept(ctx context.Context, project, recordID, reason, agentID string) {
	if _, err := l.Memory.Accept(ctx, project, recordID, reason, agentID); err != nil {
		l.logEvent(invocation.EventInvocationFailed, map[string]any{"project": project, "stage": "memory_accept", "error": err.Error()})
		return
	}
	l.logEvent(invocation.EventMemoryWritten, map[string]any{"project": project, "stage": "memory_accept", "record_id": recordID})
}

func (l *AriLoop) reject(ctx context.Context, project, recordID, reason, agentID string) {
	if _, err := l.Memory.Reject(ctx, project, recordID, reason, agentID); err != nil {
		l.logEvent(invocation.EventInvocationFailed, map[string]any{"project": project, "stage": "memory_reject", "error": err.Error()})
		return
	}
	l.logEvent(invocation.EventMemoryWritten, map[string]any{"project": project, "stage": "memory_reject", "record_id": recordID})
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
