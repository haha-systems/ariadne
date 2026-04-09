package runstate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// LogEntry is a parsed line from run.jsonl.
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Event     string `json:"event"`
	Line      string `json:"line,omitempty"`
}

// TailLog returns the last n parsed JSONL entries from a run log.
func TailLog(path string, n int) ([]LogEntry, error) {
	if n <= 0 {
		n = 20
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", path, err)
	}
	defer f.Close()

	entries := make([]LogEntry, 0, n)
	scanner := bufio.NewScanner(f)
	const maxTokenSize = 1024 * 1024
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxTokenSize)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			entry = LogEntry{Event: "raw", Line: line}
		}
		entries = append(entries, entry)
		if len(entries) > n {
			entries = entries[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan log %s: %w", path, err)
	}
	return entries, nil
}
