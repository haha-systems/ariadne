package session

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/haha-systems/ariadne/internal/control"
)

// ChangeKind is what happened to a session in the host.
type ChangeKind string

const (
	// ChangeAdded means a session was registered.
	ChangeAdded ChangeKind = "added"
	// ChangeUpdated means a registered session's descriptor changed.
	ChangeUpdated ChangeKind = "updated"
	// ChangeRemoved means a session was unregistered. Its controller must
	// not be used afterwards.
	ChangeRemoved ChangeKind = "removed"
)

// Change is one mutation of the host's session set. It carries the whole
// descriptor rather than an ID, so a switcher can render the new state
// without a round-trip back to the host for every keystroke of activity.
type Change struct {
	Kind       ChangeKind
	Descriptor Descriptor
}

// Host is the set of sessions a harness can show. It is the multiplexer
// that makes "one harness for everything" possible: the issue workflow and
// the research runtime each register sessions here, and the interface talks
// only to this.
type Host interface {
	// List returns every known session, active ones first, then most
	// recently updated.
	List(ctx context.Context) ([]Descriptor, error)

	// Open returns the controller for one session, or ErrNoSuchSession.
	Open(ctx context.Context, id string) (Controller, error)

	// Watch streams the session set's mutations. The channel closes when
	// ctx is cancelled or the host is closed. A subscriber that stops
	// reading is dropped rather than allowed to block a workflow.
	Watch(ctx context.Context) (<-chan Change, error)
}

// watchBuffer is how far a slow switcher may lag before it starts losing
// changes. Session churn is orders of magnitude slower than event or log
// traffic, so a small buffer is generous.
const watchBuffer = 64

// Registry is the in-process Host. Workflows register sessions as they
// start them and unregister as they finish; the harness observes.
//
// It is safe for concurrent use, and every method is non-blocking with
// respect to the workflows that call it: a stalled interface can never
// stall agent work.
type Registry struct {
	mu       sync.RWMutex
	closed   bool
	sessions map[string]*entry
	watchers map[int]chan Change
	nextID   int

	// now is injectable so tests can pin UpdatedAt.
	now func() time.Time
}

type entry struct {
	desc       Descriptor
	controller Controller
}

// NewRegistry constructs an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		sessions: make(map[string]*entry),
		watchers: make(map[int]chan Change),
		now:      time.Now,
	}
}

// Register adds a session. The descriptor's ID must be non-empty and unused.
// StartedAt and UpdatedAt are filled in when the caller left them zero, so a
// workflow need only supply identity.
func (r *Registry) Register(desc Descriptor, controller Controller) error {
	if desc.ID == "" {
		return fmt.Errorf("%w: empty session id", control.ErrInvalidCommand)
	}
	if controller == nil {
		return fmt.Errorf("%w: nil controller for session %s", control.ErrInvalidCommand, desc.ID)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	if _, exists := r.sessions[desc.ID]; exists {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrDuplicateSession, desc.ID)
	}

	now := r.now()
	if desc.StartedAt.IsZero() {
		desc.StartedAt = now
	}
	desc.UpdatedAt = now

	r.sessions[desc.ID] = &entry{desc: desc, controller: controller}
	r.broadcastLocked(Change{Kind: ChangeAdded, Descriptor: desc})
	r.mu.Unlock()
	return nil
}

// Update replaces a registered session's descriptor, preserving its ID and
// StartedAt. Workflows call it as work progresses so the switcher stays
// truthful without polling.
func (r *Registry) Update(desc Descriptor) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	e, ok := r.sessions[desc.ID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNoSuchSession, desc.ID)
	}

	desc.StartedAt = e.desc.StartedAt
	desc.UpdatedAt = r.now()
	e.desc = desc

	r.broadcastLocked(Change{Kind: ChangeUpdated, Descriptor: desc})
	r.mu.Unlock()
	return nil
}

// Unregister removes a session. Removing an unknown session is not an
// error: a workflow cleaning up after a failure should not have to know
// whether it got as far as registering.
func (r *Registry) Unregister(id string) {
	r.mu.Lock()
	e, ok := r.sessions[id]
	if !ok || r.closed {
		r.mu.Unlock()
		return
	}
	delete(r.sessions, id)
	r.broadcastLocked(Change{Kind: ChangeRemoved, Descriptor: e.desc})
	r.mu.Unlock()
}

// List implements Host.
func (r *Registry) List(ctx context.Context) ([]Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, ErrClosed
	}

	out := make([]Descriptor, 0, len(r.sessions))
	for _, e := range r.sessions {
		out = append(out, e.desc)
	}

	// Active sessions first, then most recently updated, then by ID so the
	// order never flickers between reads of an unchanged set.
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].Active(), out[j].Active(); a != b {
			return a
		}
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Open implements Host.
func (r *Registry) Open(ctx context.Context, id string) (Controller, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, ErrClosed
	}
	e, ok := r.sessions[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoSuchSession, id)
	}
	return e.controller, nil
}

// Watch implements Host.
func (r *Registry) Watch(ctx context.Context) (<-chan Change, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrClosed
	}
	id := r.nextID
	r.nextID++
	ch := make(chan Change, watchBuffer)
	r.watchers[id] = ch
	r.mu.Unlock()

	go func() {
		<-ctx.Done()
		r.dropWatcher(id)
	}()
	return ch, nil
}

func (r *Registry) dropWatcher(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.watchers[id]; ok {
		delete(r.watchers, id)
		close(ch)
	}
}

// broadcastLocked fans a change out to watchers. The caller holds the write
// lock. A watcher whose buffer is full loses the change rather than
// blocking the workflow that produced it — the switcher can always recover
// the truth with List, but a stalled agent run cannot be recovered at all.
func (r *Registry) broadcastLocked(change Change) {
	for _, ch := range r.watchers {
		select {
		case ch <- change:
		default:
		}
	}
}

// Close releases every watcher. Registered sessions are dropped; their
// controllers are owned by whoever started them and are not stopped here,
// because the host observes sessions and must never be the thing that ends
// one.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	for id, ch := range r.watchers {
		delete(r.watchers, id)
		close(ch)
	}
	r.sessions = make(map[string]*entry)
}
