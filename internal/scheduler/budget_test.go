package scheduler

import (
	"testing"
	"time"
)

func TestBudget_CanEscalate_NoLimitsAlwaysAllows(t *testing.T) {
	b := NewBudget(0, 0, 0)
	for i := 0; i < 100; i++ {
		if !b.CanEscalate() {
			t.Fatalf("expected unlimited budget to always allow escalation (iteration %d)", i)
		}
		b.RecordEscalation()
	}
}

func TestBudget_PerCycleMax_Exhaustion(t *testing.T) {
	b := NewBudget(2, 0, 0)
	if !b.CanEscalate() {
		t.Fatal("expected first escalation to be allowed")
	}
	b.RecordEscalation()
	if !b.CanEscalate() {
		t.Fatal("expected second escalation to be allowed")
	}
	b.RecordEscalation()
	if b.CanEscalate() {
		t.Fatal("expected third escalation to be denied (per-cycle max reached)")
	}
}

func TestBudget_ResetCycle_RestoresPerCycleAllowance(t *testing.T) {
	b := NewBudget(1, 0, 0)
	b.RecordEscalation()
	if b.CanEscalate() {
		t.Fatal("expected per-cycle max reached")
	}
	b.ResetCycle()
	if !b.CanEscalate() {
		t.Fatal("expected ResetCycle to restore per-cycle allowance")
	}
}

func TestBudget_PerSessionMax_NotResetByCycle(t *testing.T) {
	b := NewBudget(0, 1, 0)
	b.RecordEscalation()
	if b.CanEscalate() {
		t.Fatal("expected per-session max reached")
	}
	b.ResetCycle()
	if b.CanEscalate() {
		t.Fatal("ResetCycle must not restore the per-session allowance")
	}
}

func TestBudget_Cooldown_UsesInjectedClock(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := NewBudget(0, 0, 10*time.Minute)
	b.nowFunc = func() time.Time { return now }

	b.RecordEscalation()
	if b.CanEscalate() {
		t.Fatal("expected cooldown to deny an immediate second escalation")
	}

	now = now.Add(5 * time.Minute)
	if b.CanEscalate() {
		t.Fatal("expected cooldown still active after 5 of 10 minutes")
	}

	now = now.Add(6 * time.Minute) // 11 minutes total
	if !b.CanEscalate() {
		t.Fatal("expected cooldown to have elapsed after 11 minutes")
	}
}

func TestBudget_Snapshot(t *testing.T) {
	b := NewBudget(3, 10, time.Minute)
	b.RecordEscalation()
	snap := b.Snapshot()
	if snap.CycleCount != 1 || snap.PerCycleMax != 3 {
		t.Errorf("cycle snapshot = %+v, want CycleCount=1 PerCycleMax=3", snap)
	}
	if snap.SessionCount != 1 || snap.PerSessionMax != 10 {
		t.Errorf("session snapshot = %+v, want SessionCount=1 PerSessionMax=10", snap)
	}
	if snap.Cooldown != time.Minute {
		t.Errorf("Cooldown = %v, want 1m", snap.Cooldown)
	}
	if snap.LastEscalation.IsZero() {
		t.Error("expected LastEscalation to be set after RecordEscalation")
	}
}
