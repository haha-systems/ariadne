package scheduler

import "testing"

func TestGuardAgainstRecursion_Depth0AllowsAstra(t *testing.T) {
	req := DecisionRequest{Depth: 0, Options: []Action{{Name: "escalate", ProfileName: "astra"}}}
	if err := guardAgainstRecursion(req, "astra"); err != nil {
		t.Errorf("expected depth 0 to allow offering astra, got error: %v", err)
	}
}

func TestGuardAgainstRecursion_Depth1RejectsAstra(t *testing.T) {
	req := DecisionRequest{Depth: 1, Options: []Action{{Name: "escalate again", ProfileName: "astra"}}}
	if err := guardAgainstRecursion(req, "astra"); err == nil {
		t.Fatal("expected depth 1 to reject offering astra (would recurse)")
	}
}

func TestGuardAgainstRecursion_Depth1AllowsNonAstraOptions(t *testing.T) {
	req := DecisionRequest{Depth: 1, Options: []Action{{Name: "continue", ProfileName: "ari"}}}
	if err := guardAgainstRecursion(req, "astra"); err != nil {
		t.Errorf("expected depth 1 to allow non-astra options, got error: %v", err)
	}
}

func TestGuardAgainstRecursion_NoAstraProfileNameConfigured(t *testing.T) {
	req := DecisionRequest{Depth: 5, Options: []Action{{Name: "x", ProfileName: "anything"}}}
	if err := guardAgainstRecursion(req, ""); err != nil {
		t.Errorf("expected the guard to be a no-op when astraProfileName is empty, got error: %v", err)
	}
}
