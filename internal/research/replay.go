package research

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/haha-systems/ariadne/internal/control"
)

// JSONLReplayer reads recent events back out of a run's durable JSONL log.
//
// This is a catch-up mechanism, not a subscription: an interface opening
// mid-run replays the tail once for context, then subscribes to the live
// bus. Tailing the file as the primary real-time mechanism would make the
// UI's liveness depend on filesystem flush timing, and would reparse text
// the runtime already has in memory.
type JSONLReplayer struct {
	Path string
	// MaxScanBytes bounds one log line, guarding against a pathological
	// line (a provider dumping a huge blob) exhausting memory on replay.
	MaxScanBytes int
}

var _ EventReplayer = (*JSONLReplayer)(nil)

// Replay returns up to limit of the most recent events. A missing log is
// not an error — a project that has never run simply has no history.
func (j *JSONLReplayer) Replay(_ context.Context, limit int) ([]control.Event, error) {
	f, err := os.Open(j.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("research: replay events: %w", err)
	}
	defer f.Close()

	maxBytes := j.MaxScanBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxBytes)

	var events []control.Event
	for scanner.Scan() {
		var fields map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &fields); err != nil {
			continue // a truncated or malformed line is skipped, never fatal
		}
		ev := eventFromFields(fields)
		if ev.Name == "" {
			continue
		}
		events = append(events, ev)
		if limit > 0 && len(events) > limit {
			events = events[len(events)-limit:]
		}
	}
	if err := scanner.Err(); err != nil {
		// Return what was parsed: partial history beats none.
		return events, nil
	}
	return events, nil
}

// eventFromFields converts one persisted JSONL object back into an Event,
// lifting the reserved "event" and "timestamp" keys out of the field map so
// a consumer sees the same shape a live subscriber does.
func eventFromFields(fields map[string]any) control.Event {
	ev := control.Event{Fields: make(map[string]any, len(fields))}
	for k, v := range fields {
		switch k {
		case "event":
			ev.Name, _ = v.(string)
		case "timestamp":
			if s, ok := v.(string); ok {
				if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
					ev.Time = t
				}
			}
		default:
			ev.Fields[k] = v
		}
	}
	return ev
}
