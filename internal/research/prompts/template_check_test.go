package prompts

import (
	"os"
	"strings"
	"testing"
)

// TestGuidanceTemplateComposes checks the shipped guidance template against
// the built-in prompt: it must be fenced as reference material, must not
// reassign identity, and must not duplicate the sections the builder
// already supplies.
func TestGuidanceTemplateComposes(t *testing.T) {
	const path = "../../../.ariadne/research/GUIDANCE.template.md"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("template not present: %v", err)
	}
	guidance := string(data)

	prompt := BuildAri(AriInput{Project: "p1", Guidance: guidance})

	// The template mentions Astra by name while describing the team. That
	// must remain harmless: identity is asserted before the fence and
	// re-asserted after it.
	identityAt := strings.Index(prompt, "You are Ari.")
	guidanceAt := strings.Index(prompt, "Research guidance — template")
	if identityAt < 0 || guidanceAt < 0 || identityAt > guidanceAt {
		t.Fatalf("identity must precede the guidance fence (identity=%d guidance=%d)", identityAt, guidanceAt)
	}
	if !strings.Contains(prompt[guidanceAt:], "You are Ari, the routine research agent. You are not Astra.") {
		t.Error("identity is not re-asserted after the guidance block")
	}
	if !strings.Contains(prompt, "<<<GUIDANCE") {
		t.Error("guidance is not fenced as reference material")
	}

	// The template must not restate what the builder already provides;
	// duplicating these is paid for on every single cycle.
	for _, duplicated := range []string{
		"$ARIADNE_OUTPUT_FILE",
		"Perform exactly one bounded research step.",
	} {
		if strings.Contains(guidance, duplicated) {
			t.Errorf("template duplicates built-in prompt text: %q", duplicated)
		}
	}

	// And the built-in sections must still all be present around it.
	for _, want := range []string{
		"YOUR IDENTITY", "SCOPE:", "YOUR TASK THIS INVOCATION",
		"ESCALATION RULES", "REPORTING:",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("composed prompt lost built-in section %q", want)
		}
	}
}
