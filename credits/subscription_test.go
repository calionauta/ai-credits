package credits

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSubscriptionLifecycle(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Not found initially.
	if _, err := s.Subscription(ctx, testUser); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("Subscription = %v, want ErrSubscriptionNotFound", err)
	}

	// Set active.
	if err := s.SetSubscription(ctx, testUser, "pro", SubscriptionActive); err != nil {
		t.Fatalf("SetSubscription: %v", err)
	}
	sub, err := s.Subscription(ctx, testUser)
	if err != nil {
		t.Fatalf("Subscription: %v", err)
	}
	if sub.Plan != "pro" || sub.Status != SubscriptionActive {
		t.Fatalf("sub = %+v, want pro/active", sub)
	}

	// Cancel keeps the plan, gates the grant.
	if err := s.CancelSubscription(ctx, testUser); err != nil {
		t.Fatalf("CancelSubscription: %v", err)
	}
	sub, _ = s.Subscription(ctx, testUser)
	if sub.Status != SubscriptionCanceled {
		t.Fatalf("after cancel status = %q, want cancelled", sub.Status)
	}
}

func TestInvalidSubscriptionStatusRejected(t *testing.T) {
	s, _ := newTestService(t)
	if err := s.SetSubscription(context.Background(), testUser, "pro", "nope"); err == nil {
		t.Fatal("expected invalid status to error")
	}
}

func TestMonthlyGrantSkippedForNonActivePlan(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	s.cfg.MonthlyCredits = 0
	s.cfg.PlanMonthlyCredits = map[string]int64{"pro": 2000, "lite": 500}

	// No subscription → still grants (backward compat).
	ok, err := s.EnsureMonthlyGrant(ctx, testUser, "pro")
	if err != nil || !ok {
		t.Fatalf("expected unconditional grant, ok=%v err=%v", ok, err)
	}
}

// The entitlement gate: a cancelled/paused subscription stops new grants.
func TestMonthlyGrantSubscriptionGate(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	s.cfg.Now = func() time.Time { return now }
	s.cfg.MonthlyCredits = 0
	s.cfg.PlanMonthlyCredits = map[string]int64{"pro": 2000}

	// Active subscription grants in period 2026-08.
	if err := s.SetSubscription(ctx, testUser, "pro", SubscriptionActive); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.EnsureMonthlyGrant(ctx, testUser, "pro"); !ok {
		t.Fatal("expected grant for active sub")
	}
	bal, _ := s.Balance(ctx, testUser)
	if bal != 2000 {
		t.Fatalf("balance = %d, want 2000 after active grant", bal)
	}

	// Cancel before next period → 2026-09 refuses grant, no balance change.
	now = now.AddDate(0, 1, 0)
	if err := s.CancelSubscription(ctx, testUser); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.EnsureMonthlyGrant(ctx, testUser, "pro"); ok {
		t.Fatal("expected NO grant for cancelled sub")
	}
	bal, _ = s.Balance(ctx, testUser)
	if bal != 2000 {
		t.Fatalf("balance = %d, want 2000 (unchanged) after cancelled grant attempt", bal)
	}
}

// Paused subscription behaves like cancelled for the gate.
func TestMonthlyGrantPausedBlocks(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	s.cfg.Now = func() time.Time { return now }
	s.cfg.MonthlyCredits = 1000
	s.cfg.PlanMonthlyCredits = nil

	if err := s.SetSubscription(ctx, testUser, "pro", SubscriptionPaused); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.EnsureMonthlyGrant(ctx, testUser, "pro"); ok {
		t.Fatal("expected NO grant for paused sub")
	}
}
