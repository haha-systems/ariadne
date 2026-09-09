package control

import (
	"context"
	"sync"
	"time"
)

// defaultBufferSize is how many messages a subscriber may fall behind by
// before it starts losing them. It is generous enough that a UI redrawing
// at human speed never drops anything, and bounded so a stuck subscriber
// cannot grow memory without limit.
const defaultBufferSize = 256

// Bus broadcasts structured runtime events to live subscribers.
//
// The central guarantee is that publishing never blocks. Research is the
// system's real work; an interface is an observer of it. If a subscriber
// stops reading — a UI mid-redraw, a wedged terminal, a paused debugger —
// the bus drops that subscriber's backlog rather than applying backpressure
// to the research loop. Durability is not the bus's job: the JSONL sink
// alongside it is the record that must not lose events.
type Bus struct {
	mu     sync.RWMutex
	subs   map[int]chan Event
	nextID int
	closed bool

	bufferSize int

	// dropped counts events discarded for slow subscribers, so the loss is
	// observable rather than silent.
	dropped int
}

// NewBus constructs an empty Bus.
func NewBus() *Bus { return &Bus{subs: make(map[int]chan Event), bufferSize: defaultBufferSize} }

// Log implements the workflow-facing event sink signature, so a Bus can sit
// directly alongside the JSONL writer in a MultiSink.
func (b *Bus) Log(event string, fields map[string]any) {
	b.Publish(Event{Name: event, Time: time.Now().UTC(), Fields: fields})
}

// Publish delivers ev to every current subscriber, skipping any whose
// buffer is full.
func (b *Bus) Publish(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Slow subscriber: drop rather than block research.
			b.dropped++
		}
	}
}

// Subscribe returns a channel of events, closed when ctx is cancelled or
// the Bus is closed. Callers must cancel ctx when finished; a subscription
// left open holds a buffer for the life of the process.
func (b *Bus) Subscribe(ctx context.Context) <-chan Event {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		ch := make(chan Event)
		close(ch)
		return ch
	}

	id := b.nextID
	b.nextID++
	ch := make(chan Event, b.bufferSize)
	b.subs[id] = ch
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.unsubscribe(id)
	}()
	return ch
}

func (b *Bus) unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.subs[id]
	if !ok {
		return
	}
	delete(b.subs, id)
	close(ch)
}

// Close shuts the bus down and closes every subscriber channel.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}

// Dropped reports how many events have been discarded for slow
// subscribers.
func (b *Bus) Dropped() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.dropped
}

// Subscribers reports the current subscriber count.
func (b *Bus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// LogBus broadcasts agent output under the same never-block-research
// contract as Bus, and additionally retains a bounded ring of recent lines
// per agent so an interface attaching mid-run can render immediate context
// instead of an empty pane.
type LogBus struct {
	mu     sync.RWMutex
	subs   map[int]chan AgentLogEntry
	nextID int
	closed bool

	// history is a per-agent ring of recent entries.
	history    map[string][]AgentLogEntry
	order      []AgentLogEntry
	maxHistory int
	dropped    int
}

// NewLogBus constructs a LogBus retaining maxHistory entries per agent.
func NewLogBus(maxHistory int) *LogBus {
	if maxHistory <= 0 {
		maxHistory = 1000
	}
	return &LogBus{
		subs:       make(map[int]chan AgentLogEntry),
		history:    make(map[string][]AgentLogEntry),
		maxHistory: maxHistory,
	}
}

// Publish records entry in history and delivers it to live subscribers.
func (b *LogBus) Publish(entry AgentLogEntry) {
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.history[entry.Agent] = appendBounded(b.history[entry.Agent], entry, b.maxHistory)
	b.order = appendBounded(b.order, entry, b.maxHistory*2)
	subs := make([]chan AgentLogEntry, 0, len(b.subs))
	for _, ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- entry:
		default:
			b.mu.Lock()
			b.dropped++
			b.mu.Unlock()
		}
	}
}

// Subscribe returns a channel of log entries, closed when ctx is cancelled.
func (b *LogBus) Subscribe(ctx context.Context) <-chan AgentLogEntry {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		ch := make(chan AgentLogEntry)
		close(ch)
		return ch
	}
	id := b.nextID
	b.nextID++
	ch := make(chan AgentLogEntry, defaultBufferSize)
	b.subs[id] = ch
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		defer b.mu.Unlock()
		if existing, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(existing)
		}
	}()
	return ch
}

// History returns up to limit recent entries for agent, or across every
// agent in arrival order when agent is "".
func (b *LogBus) History(agent string, limit int) []AgentLogEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()

	src := b.order
	if agent != "" {
		src = b.history[agent]
	}
	if limit > 0 && len(src) > limit {
		src = src[len(src)-limit:]
	}
	return append([]AgentLogEntry(nil), src...)
}

// Close shuts the bus down and closes every subscriber channel.
func (b *LogBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}

// Dropped reports how many log entries have been discarded.
func (b *LogBus) Dropped() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.dropped
}

// Writer returns an io.Writer that publishes an agent's process output into
// this bus, one entry per line. Wiring a provider's output through it is
// what keeps each agent's stream independently readable.
func (b *LogBus) Writer(project, agent, stream string) *LogWriter {
	return &LogWriter{bus: b, project: project, agent: agent, stream: stream}
}

// LogWriter splits an agent's output into lines and publishes each one.
type LogWriter struct {
	bus     *LogBus
	project string
	agent   string
	stream  string

	mu  sync.Mutex
	buf []byte
}

// Write implements io.Writer. A partial line is held until its newline
// arrives, so a log pane never renders half a line.
func (w *LogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	var lines []string
	for {
		idx := indexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		lines = append(lines, string(w.buf[:idx]))
		w.buf = w.buf[idx+1:]
	}
	w.mu.Unlock()

	for _, line := range lines {
		w.bus.Publish(AgentLogEntry{
			Project: w.project, Agent: w.agent, Stream: w.stream, Text: line,
		})
	}
	return len(p), nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func appendBounded[T any](buf []T, item T, max int) []T {
	buf = append(buf, item)
	if len(buf) > max {
		buf = buf[len(buf)-max:]
	}
	return buf
}
