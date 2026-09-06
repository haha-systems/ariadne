package scheduler

import (
	"sync"
	"time"
)

// BudgetSnapshot is a read-only view of a Budget's current state, suitable
// for the `ariadne budget` CLI command and for inclusion in an Astra
// research packet.
type BudgetSnapshot struct {
	CycleCount     int
	PerCycleMax    int
	SessionCount   int
	PerSessionMax  int
	Cooldown       time.Duration
	LastEscalation time.Time
}

// Budget enforces the quota-safety limits every scarce agent needs: a cap
// per workflow cycle, a cap per session, and a cooldown between calls. A
// zero max (PerCycleMax/PerSessionMax) means "unlimited" for that dimension.
type Budget struct {
	perCycleMax   int
	perSessionMax int
	cooldown      time.Duration
	nowFunc       func() time.Time // overridable in tests

	mu             sync.Mutex
	cycleCount     int
	sessionCount   int
	lastEscalation time.Time
}

// NewBudget constructs a Budget. cooldown <= 0 disables cooldown enforcement.
func NewBudget(perCycleMax, perSessionMax int, cooldown time.Duration) *Budget {
	return &Budget{
		perCycleMax:   perCycleMax,
		perSessionMax: perSessionMax,
		cooldown:      cooldown,
		nowFunc:       time.Now,
	}
}

// CanEscalate reports whether another escalation is currently allowed.
func (b *Budget) CanEscalate() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.perCycleMax > 0 && b.cycleCount >= b.perCycleMax {
		return false
	}
	if b.perSessionMax > 0 && b.sessionCount >= b.perSessionMax {
		return false
	}
	if b.cooldown > 0 && !b.lastEscalation.IsZero() && b.nowFunc().Sub(b.lastEscalation) < b.cooldown {
		return false
	}
	return true
}

// RecordEscalation increments both counters and resets the cooldown clock.
// Callers must have already checked CanEscalate.
func (b *Budget) RecordEscalation() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.cycleCount++
	b.sessionCount++
	b.lastEscalation = b.nowFunc()
}

// ResetCycle zeroes the per-cycle counter at the start of a new workflow
// cycle. The per-session counter and cooldown are unaffected.
func (b *Budget) ResetCycle() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cycleCount = 0
}

// Snapshot returns the current state for display/inspection.
func (b *Budget) Snapshot() BudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	return BudgetSnapshot{
		CycleCount:     b.cycleCount,
		PerCycleMax:    b.perCycleMax,
		SessionCount:   b.sessionCount,
		PerSessionMax:  b.perSessionMax,
		Cooldown:       b.cooldown,
		LastEscalation: b.lastEscalation,
	}
}
