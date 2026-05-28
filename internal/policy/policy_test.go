package policy

import (
	"context"
	"testing"

	"github.com/haha-systems/ariadne/internal/gateway"
)

func TestNoopEngine_DoesNothing(t *testing.T) {
	eng := NoopEngine{}

	inv := gateway.Invocation{Title: "test", Prompt: "do something"}

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

	run := &gateway.Run{ID: "run_1", Status: "succeeded"}
	if err := eng.PostRun(context.Background(), run, inv); err != nil {
		t.Errorf("PostRun: %v", err)
	}
}
