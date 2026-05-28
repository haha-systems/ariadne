package policy

import (
	"context"
	"testing"
)

func TestNoopEngine_DoesNothing(t *testing.T) {
	eng := NoopEngine{}

	inv := Invocation{Title: "test", Prompt: "do something"}

	decision, err := eng.SelectRoute(context.Background(), inv)
	if err != nil {
		t.Fatalf("SelectRoute: %v", err)
	}
	if decision != nil {
		t.Errorf("expected nil decision from NoopEngine, got %+v", decision)
	}

	if err := eng.PreRun(context.Background(), &inv); err != nil {
		t.Errorf("PreRun: %v", err)
	}

	run := RunSummary{ID: "run_1", Status: "succeeded"}
	if err := eng.PostRun(context.Background(), run, inv); err != nil {
		t.Errorf("PostRun: %v", err)
	}
}
