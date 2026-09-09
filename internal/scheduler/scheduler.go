// Package scheduler implements Quota-Aware Cognition (QAC): a deterministic,
// inspectable decision of whether to continue with the current agent or
// escalate to a scarcer, more capable one. It knows nothing about GitHub,
// issues, or research — it only reasons about agentprofile.CostClass /
// ScarcityClass and a caller-supplied Weight, so it stays reusable across
// every future workflow, not just the research one.
package scheduler

import (
	"context"
	"fmt"

	"github.com/haha-systems/ariadne/internal/agentprofile"
)

// Action is one candidate a DecisionRequest offers the scheduler: "continue
// with Ari" and "escalate to Astra" are both Actions. Weight is computed by
// the caller (e.g. the research workflow) and represents how strongly that
// action is currently favored; the scheduler only adjusts for scarcity,
// budget, and control mode on top of it.
type Action struct {
	Name          string
	ProfileName   string
	CostClass     agentprofile.CostClass
	ScarcityClass agentprofile.ScarcityClass
	Weight        float64
}

// DecisionRequest asks the scheduler to choose among Options. Context is a
// human-readable explanation of why escalation is even being considered —
// required whenever the best-weighted option is not ScarcityPlentiful, so an
// escalation reason is never silently absent. Depth is 0 for a routine
// invocation and >=1 for an invocation already made on behalf of a prior
// escalation.
type DecisionRequest struct {
	Context      string
	Options      []Action
	EvidenceRefs []string
	Depth        int
}

// Decision is the scheduler's inspectable output.
type Decision struct {
	Chosen    Action
	Score     float64
	Reason    string
	Escalated bool
}

// Scheduler implements Quota-Aware Cognition.
type Scheduler struct {
	mode             ControlMode
	budget           *Budget
	approver         Approver
	astraProfileName string
}

// New constructs a Scheduler. budget must not be nil. approver may be nil
// unless mode is ControlManual. astraProfileName names the scarce profile
// the recursion guard protects; pass "" to disable the guard (e.g. in tests
// with no scarce profile at all).
func New(mode ControlMode, budget *Budget, approver Approver, astraProfileName string) *Scheduler {
	return &Scheduler{mode: mode, budget: budget, approver: approver, astraProfileName: astraProfileName}
}

// Decide picks the best Action among req.Options. Selecting any Action
// whose ScarcityClass is not ScarcityPlentiful is treated as an escalation
// and is gated by control mode and Budget; if denied, Decide falls back to
// the best ScarcityPlentiful option instead of failing the whole request.
func (s *Scheduler) Decide(ctx context.Context, req DecisionRequest) (Decision, error) {
	if err := guardAgainstRecursion(req, s.astraProfileName); err != nil {
		return Decision{}, err
	}
	if len(req.Options) == 0 {
		return Decision{}, fmt.Errorf("scheduler: no candidate actions provided")
	}

	best := bestByWeight(req.Options)
	if best.ScarcityClass == agentprofile.ScarcityPlentiful {
		return Decision{Chosen: best, Score: best.Weight, Reason: req.Context, Escalated: false}, nil
	}

	// Selecting a non-plentiful action is an escalation: never proceed
	// without a stated reason (no hidden escalation reasons).
	if req.Context == "" {
		return Decision{}, fmt.Errorf("scheduler: escalating to %q requires a non-empty Context", best.Name)
	}

	approved, denyReason, err := s.authorize(ctx, req)
	if err != nil {
		return Decision{}, fmt.Errorf("scheduler: authorize escalation: %w", err)
	}
	if !approved {
		if fallback, ok := plentifulFallback(req.Options); ok {
			return Decision{Chosen: fallback, Score: fallback.Weight, Reason: denyReason, Escalated: false}, nil
		}
		return Decision{}, fmt.Errorf("scheduler: escalation denied (%s) and no fallback action available", denyReason)
	}

	s.budget.RecordEscalation()
	return Decision{Chosen: best, Score: best.Weight, Reason: req.Context, Escalated: true}, nil
}

// authorize applies control-mode policy plus Budget to an escalation
// candidate. A false return is never an error — it is a normal, explained
// denial the caller (Decide) turns into a fallback.
func (s *Scheduler) authorize(ctx context.Context, req DecisionRequest) (approved bool, reason string, err error) {
	switch s.mode {
	case ControlDisabled:
		return false, "escalation disabled by control mode", nil

	case ControlManual:
		if s.approver == nil {
			return false, "manual approval required but no approver is configured", nil
		}
		if !s.budget.CanEscalate() {
			return false, "budget exhausted or cooldown active", nil
		}
		return s.approver.RequestApproval(ctx, req)

	case ControlAutomatic:
		if !s.budget.CanEscalate() {
			return false, "budget exhausted or cooldown active", nil
		}
		return true, "", nil

	default:
		return false, fmt.Sprintf("unknown control mode %q", s.mode), nil
	}
}

func bestByWeight(options []Action) Action {
	best := options[0]
	for _, opt := range options[1:] {
		if opt.Weight > best.Weight {
			best = opt
		}
	}
	return best
}

func plentifulFallback(options []Action) (Action, bool) {
	var best Action
	found := false
	for _, opt := range options {
		if opt.ScarcityClass != agentprofile.ScarcityPlentiful {
			continue
		}
		if !found || opt.Weight > best.Weight {
			best = opt
			found = true
		}
	}
	return best, found
}

// BudgetSnapshot exposes the scheduler's current quota state so callers can
// record *why* an escalation decision went the way it did — a denial for
// "budget exhausted" is only interpretable alongside the counts that
// exhausted it.
func (s *Scheduler) BudgetSnapshot() BudgetSnapshot {
	return s.budget.Snapshot()
}
