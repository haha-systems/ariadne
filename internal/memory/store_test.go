package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ariadne-memory-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	path := filepath.Join(tmpDir, "memory.json")
	store := New(path)

	// Test Remember
	err = store.Remember("foo", "bar", "run_1")
	if err != nil {
		t.Errorf("Remember failed: %v", err)
	}

	// Test Recall
	val, ok, err := store.Recall("foo")
	if err != nil {
		t.Errorf("Recall failed: %v", err)
	}
	if !ok || val != "bar" {
		t.Errorf("Recall returned wrong value: got %q, %v, want %q, true", val, ok, "bar")
	}

	// Test Update
	err = store.Remember("foo", "baz", "run_2")
	if err != nil {
		t.Errorf("Remember update failed: %v", err)
	}
	val, ok, err = store.Recall("foo")
	if !ok || val != "baz" {
		t.Errorf("Recall returned wrong updated value: got %q, %v, want %q, true", val, ok, "baz")
	}

	// Test List
	entries, err := store.List()
	if err != nil {
		t.Errorf("List failed: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("List returned wrong number of entries: got %d, want 1", len(entries))
	}
	if entries[0].Key != "foo" || entries[0].Value != "baz" {
		t.Errorf("List entry is wrong: %+v", entries[0])
	}

	// Test Forget
	err = store.Forget("foo")
	if err != nil {
		t.Errorf("Forget failed: %v", err)
	}
	_, ok, err = store.Recall("foo")
	if ok {
		t.Errorf("Recall should have failed after Forget")
	}
}
