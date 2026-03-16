package supervisor

import (
	"testing"

	"github.com/haha-systems/conductor/internal/domain"
)

func TestMergeEnv(t *testing.T) {
	global := map[string]string{"A": "1", "B": "2"}
	perTask := map[string]string{"B": "override", "C": "3"}

	merged := mergeEnv(global, perTask)

	if merged["A"] != "1" {
		t.Errorf("expected A=1, got %s", merged["A"])
	}
	if merged["B"] != "override" {
		t.Errorf("expected B=override (per-task wins), got %s", merged["B"])
	}
	if merged["C"] != "3" {
		t.Errorf("expected C=3, got %s", merged["C"])
	}
}

func TestMergeEnv_NilPerTask(t *testing.T) {
	global := map[string]string{"X": "y"}
	merged := mergeEnv(global, nil)
	if merged["X"] != "y" {
		t.Errorf("expected X=y, got %s", merged["X"])
	}
}

func TestTaskEnv_NilConfig(t *testing.T) {
	task := &domain.Task{}
	env := taskEnv(task)
	if env != nil {
		t.Errorf("expected nil env for task with no config, got %v", env)
	}
}

func TestTaskEnv_WithConfig(t *testing.T) {
	task := &domain.Task{
		Config: &domain.TaskConfig{
			Env: map[string]string{"NODE_ENV": "test"},
		},
	}
	env := taskEnv(task)
	if env["NODE_ENV"] != "test" {
		t.Errorf("expected NODE_ENV=test, got %s", env["NODE_ENV"])
	}
}
