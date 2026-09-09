package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guidanceFiles are the shipped guidance documents. They are checked against
// the built-in prompt because guidance is prepended to every invocation:
// anything that contradicts the runtime, or duplicates what the runtime
// already says, is paid for on every cycle of every run.
func guidanceFiles(t *testing.T) map[string]string {
	t.Helper()
	root := "../../../.ariadne/research"

	out := map[string]string{}
	for _, path := range []string{
		filepath.Join(root, "GUIDANCE.template.md"),
		filepath.Join(root, "distributed-cognition", "AGENTS.md"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out[path] = string(data)
	}
	if len(out) == 0 {
		t.Skip("no guidance files present")
	}
	return out
}

// TestGuidance_DoesNotReintroduceUnboundedWork is the regression that
// matters most here. The original mandate told the agent to work
// continuously and not to stop — correct about the research programme,
// wrong about a single invocation, and the reason QAC almost never got
// control. Guidance must not smuggle it back in.
func TestGuidance_DoesNotReintroduceUnboundedWork(t *testing.T) {
	banned := []string{
		"work continuously",
		"continue working until",
		"do not stop at a plan",
		"keep experimenting until",
		"until you reach a meaningful research result",
		"the default action is to continue working",
		"for the duration of the research task",
	}

	for path, guidance := range guidanceFiles(t) {
		lower := strings.ToLower(guidance)
		for _, phrase := range banned {
			// The distributed-cognition mandate quotes the old instruction
			// once, explicitly to reject it. That single framed mention is
			// allowed; a second is the instruction creeping back.
			if strings.Count(lower, strings.ToLower(phrase)) > 1 {
				t.Errorf("%s repeats unbounded-work instruction %q", path, phrase)
			}
		}
		if strings.Contains(lower, "do not stop after one experiment") {
			t.Errorf("%s tells a single turn not to stop", path)
		}
	}
}

// TestGuidance_DoesNotDuplicateTheRuntimeContract keeps guidance from
// restating what the builder already supplies.
func TestGuidance_DoesNotDuplicateTheRuntimeContract(t *testing.T) {
	for path, guidance := range guidanceFiles(t) {
		for _, duplicated := range []string{
			"$ARIADNE_OUTPUT_FILE",
			"Perform exactly one bounded research step.",
			`"status": "continue|escalate|complete|blocked"`,
		} {
			if strings.Contains(guidance, duplicated) {
				t.Errorf("%s duplicates built-in prompt text: %q", path, duplicated)
			}
		}
	}
}

// TestGuidance_ComposesWithTheBuiltInPrompt checks the fence holds and the
// built-in sections survive around real guidance.
func TestGuidance_ComposesWithTheBuiltInPrompt(t *testing.T) {
	for path, guidance := range guidanceFiles(t) {
		prompt := BuildAri(AriInput{Project: "distributed-cognition", Guidance: guidance})

		if !strings.Contains(prompt, "<<<GUIDANCE") {
			t.Errorf("%s: guidance is not fenced as reference material", path)
		}

		identityAt := strings.Index(prompt, "You are Ari.")
		fenceAt := strings.Index(prompt, "<<<GUIDANCE")
		if identityAt < 0 || fenceAt < 0 || identityAt > fenceAt {
			t.Errorf("%s: identity must be asserted before the guidance fence", path)
		}
		if !strings.Contains(prompt[fenceAt:], "You are Ari, the routine research agent. You are not Astra.") {
			t.Errorf("%s: identity is not re-asserted after the guidance", path)
		}

		for _, want := range []string{
			"YOUR IDENTITY", "SCOPE:", "YOUR TASK THIS INVOCATION",
			"ESCALATION RULES", "REPORTING:",
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s: composed prompt lost built-in section %q", path, want)
			}
		}
	}
}

// TestGuidance_MandateKeepsItsScientificSubstance guards the other
// direction: trimming the mandate to fit the runtime must not quietly drop
// the standards that make the research defensible.
func TestGuidance_MandateKeepsItsScientificSubstance(t *testing.T) {
	guidance, ok := guidanceFiles(t)["../../../.ariadne/research/distributed-cognition/AGENTS.md"]
	if !ok {
		t.Skip("mandate not present")
	}

	// Guidance is prose: it gets reflowed, and blockquote markers sit
	// between words. Match against a normalized copy so a phrase broken
	// across a line wrap still counts as present.
	flat := strings.Join(strings.Fields(strings.ReplaceAll(guidance, ">", " ")), " ")

	for _, want := range []string{
		"causally integrated distributed cognitive system", // the question
		"C(Mᵢ, Mⱼ, t)", // the unknown
		"speculation",  // evidence classes
		"Superadditivity",
		"Causal communication",
		"Lesion specificity",
		"Homogenization",
		"parameter-matched monolith", // baselines
		"strongest mundane",          // simpler-alternative requirement
		"Novelty",
		"seeds", // reproducibility
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("mandate lost scientific content: %q", want)
		}
	}

	// Termination must be expressed as the envelope statuses the runtime
	// actually acts on, not as free prose the loop cannot observe.
	for _, want := range []string{"`complete`", "`blocked`"} {
		if !strings.Contains(flat, want) {
			t.Errorf("mandate does not map termination onto %s", want)
		}
	}
}
