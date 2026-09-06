package scheduler

import "fmt"

// guardAgainstRecursion enforces that the scarce agent invoked on behalf of
// an escalation (Depth >= 1) can never itself be offered as a candidate — a
// scarce agent must not recursively invoke itself. Control must first
// return to the workflow/scheduler before any further escalation.
func guardAgainstRecursion(req DecisionRequest, astraProfileName string) error {
	if req.Depth < 1 || astraProfileName == "" {
		return nil
	}
	for _, opt := range req.Options {
		if opt.ProfileName == astraProfileName {
			return fmt.Errorf("scheduler: refusing to offer %q at depth %d (would recurse)", astraProfileName, req.Depth)
		}
	}
	return nil
}
