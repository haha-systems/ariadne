package invocation

// EventSink is anything that accepts a structured runtime event. It is the
// interface workflows depend on rather than *EventWriter, so an event can
// be persisted to JSONL and published to live subscribers at the same time
// without the workflow knowing either exists.
//
// *EventWriter satisfies it, which keeps the JSONL log the durable record
// it has always been.
type EventSink interface {
	Log(event string, fields map[string]any)
}

var _ EventSink = (*EventWriter)(nil)

// MultiSink fans one event out to several sinks in order.
//
// The ordering matters: put the durable JSONL sink first so an event is on
// disk before any live subscriber sees it. A subscriber that crashes after
// observing an event can then never have seen something the log does not
// contain.
type MultiSink []EventSink

var _ EventSink = (MultiSink)(nil)

// Log writes the event to every sink. Each sink receives its own copy of
// fields, because EventWriter.Log mutates the map it is given (adding
// timestamp and event keys) and sinks must not observe each other's edits.
func (m MultiSink) Log(event string, fields map[string]any) {
	for _, sink := range m {
		if sink == nil {
			continue
		}
		sink.Log(event, copyFields(fields))
	}
}

func copyFields(fields map[string]any) map[string]any {
	if fields == nil {
		return nil
	}
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		out[k] = v
	}
	return out
}
