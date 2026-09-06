package scheduler

import "context"

// ControlMode governs how much autonomy the scheduler has to escalate to a
// scarce agent on its own.
type ControlMode string

const (
	// ControlAutomatic lets the scheduler approve escalation itself, subject
	// only to Budget.
	ControlAutomatic ControlMode = "automatic"
	// ControlManual requires an Approver to sign off on every escalation,
	// even when Budget would otherwise allow it.
	ControlManual ControlMode = "manual"
	// ControlDisabled denies every escalation outright, e.g. when scarce
	// quota is nearly exhausted.
	ControlDisabled ControlMode = "disabled"
)

// Approver is consulted under ControlManual before an escalation proceeds.
type Approver interface {
	RequestApproval(ctx context.Context, req DecisionRequest) (approved bool, reason string, err error)
}
