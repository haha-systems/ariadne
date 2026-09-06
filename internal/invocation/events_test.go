package invocation

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readJSONLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var lines []map[string]any
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatalf("unmarshal line %q: %v", scanner.Text(), err)
		}
		lines = append(lines, m)
	}
	return lines
}

func TestEventWriter_Log(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	ew := NewEventWriter(f, "run_1")
	ew.Log(EventRunStarted, map[string]any{"run_id": "run_1", "provider": "codex"})
	ew.Log(EventRunSucceeded, map[string]any{"run_id": "run_1"})

	lines := readJSONLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0]["event"] != EventRunStarted {
		t.Errorf("line 0 event = %v, want %v", lines[0]["event"], EventRunStarted)
	}
	if lines[0]["provider"] != "codex" {
		t.Errorf("line 0 provider = %v, want codex", lines[0]["provider"])
	}
	if lines[0]["timestamp"] == nil || lines[0]["timestamp"] == "" {
		t.Error("expected non-empty timestamp")
	}
	if lines[1]["event"] != EventRunSucceeded {
		t.Errorf("line 1 event = %v, want %v", lines[1]["event"], EventRunSucceeded)
	}
}

func TestEventWriter_Write_StreamsProviderStdoutEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	ew := NewEventWriter(f, "run_2")

	// Write in two chunks to exercise the line-buffering logic, including a
	// partial line that only completes on the second Write call.
	if _, err := ew.Write([]byte("hello wor")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := ew.Write([]byte("ld\nsecond line\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	lines := readJSONLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("expected 2 provider_stdout events, got %d: %v", len(lines), lines)
	}
	if lines[0]["event"] != EventProviderStdout || lines[0]["line"] != "hello world" {
		t.Errorf("line 0 = %v, want event=%s line=%q", lines[0], EventProviderStdout, "hello world")
	}
	if lines[1]["line"] != "second line" {
		t.Errorf("line 1 = %v, want line=%q", lines[1], "second line")
	}
}
