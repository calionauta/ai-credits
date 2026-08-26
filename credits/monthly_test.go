package credits

import (
	"context"
	"testing"
	"time"
)

// newClockService builds a Service with an adjustable clock (start + step).
func newClockService(t *testing.T, start time.Time, step time.Duration) (*Service, func() time.Time) {
	t.Helper()
	db := testDB(t)
	var now time.Time
	now = start
	s, err := New(db, Config{
		Now:                func() time.Time { return now },
		MonthlyCredits:     1000,
		ReservationTimeout: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {})
	advance := func() time.Time {
		now = now.Add(step)
		return now
	}
	return s, advance
}

func TestEnsureMonthlyGrantOncePerPeriod(t *testing.T) {
	start := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	s, advance := newClockService(t, start, 30*24*time.Hour)
	ctx := context.Background()

	// First grant of the period.
	granted, err := s.EnsureMonthlyGrant(ctx, testUser, "free")
	if err != nil {
		t.Fatalf("EnsureMonthlyGrant: %v", err)
	}
	if !granted {
		t.Fatalf("first grant reported not-granted, want granted")
	}
	bal, _ := s.Balance(ctx, testUser)
	if bal != 1000 {
		t.Fatalf("balance after grant = %d, want 1000", bal)
	}

	// Same period → idempotent, no second grant.
	granted, err = s.EnsureMonthlyGrant(ctx, testUser, "free")
	if err != nil {
		t.Fatalf("EnsureMonthlyGrant dup: %v", err)
	}
	if granted {
		t.Fatalf("duplicate in same period reported granted")
	}
	if bal2, _ := s.Balance(ctx, testUser); bal2 != 1000 {
		t.Fatalf("balance changed on dup = %d, want 1000", bal2)
	}

	// Advance 30 days → new period → another grant.
	advance()
	granted, err = s.EnsureMonthlyGrant(ctx, testUser, "free")
	if err != nil {
		t.Fatalf("EnsureMonthlyGrant next period: %v", err)
	}
	if !granted {
		t.Fatalf("new period reported not-granted")
	}
	if bal2, _ := s.Balance(ctx, testUser); bal2 != 2000 {
		t.Fatalf("balance after 2nd period = %d, want 2000", bal2)
	}
}

func TestEnsureMonthlyGrantDisabled(t *testing.T) {
	db := testDB(t)
	s, err := New(db, Config{MonthlyCredits: 0}) // disabled
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {})
	ctx := context.Background()
	granted, err := s.EnsureMonthlyGrant(ctx, testUser, "free")
	if err != nil {
		t.Fatalf("EnsureMonthlyGrant: %v", err)
	}
	if granted {
		t.Fatalf("disabled monthly grant reported granted")
	}
}

func TestPlanMonthlyCreditsOverride(t *testing.T) {
	db := testDB(t)
	s, err := New(db, Config{
		MonthlyCredits:     1000,
		PlanMonthlyCredits: map[string]int64{"pro": 5000},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {})
	ctx := context.Background()
	if _, err := s.EnsureMonthlyGrant(ctx, testUser, "pro"); err != nil {
		t.Fatalf("EnsureMonthlyGrant: %v", err)
	}
	if bal, _ := s.Balance(ctx, testUser); bal != 5000 {
		t.Fatalf("pro balance = %d, want 5000", bal)
	}
}

func TestReconcileExpiresStaleReservation(t *testing.T) {
	start := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	s, advance := newClockService(t, start, 10*time.Minute)
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if _, err := s.Reserve(ctx, testUser, "req-1", 400); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// Advance past the 5-min timeout, then reconcile.
	advance()
	if _, err := s.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Fraction... full amount (400) returned.
	bal, _ := s.Balance(ctx, testUser)
	if bal != 1000 {
		t.Fatalf("balance after stale expiry = %d, want 1000 (full 400 returned)", bal)
	}
	// Reservation marked expired.
	var status string
	if err := s.db.QueryRowContext(ctx,
		`SELECT status FROM credit_reservations WHERE request_id='req-1'`).
		Scan(&status); err != nil {
		t.Fatalf("status: %v", err)
	}
	if status != reservationStatusExpired {
		t.Fatalf("status = %s, want expired", status)
	}
}

func TestReconcileFreshReservationNotExpired(t *testing.T) {
	start := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	s, _ := newClockService(t, start, time.Minute)
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if _, err := s.Reserve(ctx, testUser, "req-1", 400); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// Fresh (within timeout): reconcile must NOT expire it.
	if _, err := s.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var status string
	if err := s.db.QueryRowContext(ctx,
		`SELECT status FROM credit_reservations WHERE request_id='req-1'`).
		Scan(&status); err != nil {
		t.Fatalf("status: %v", err)
	}
	if status != reservationStatusReserved {
		t.Fatalf("status = %s, want reserved (fresh)", status)
	}
	// Balance still reduced by the reservation.
	if bal, _ := s.Balance(ctx, testUser); bal != 600 {
		t.Fatalf("balance = %d, want 600 (still reserved)", bal)
	}
}

func TestReconcileReportsDrift(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	// Simulate an external write that desyncs balance from ledger.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE credit_accounts SET balance = balance + 500 WHERE user_id=?`,
		testUser); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	mismatches, err := s.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(mismatches) != 1 {
		t.Fatalf("mismatches = %d, want 1", len(mismatches))
	}
	if mismatches[0].UserID != testUser || mismatches[0].Balance != 1500 || mismatches[0].LedgerSum != 1000 {
		t.Fatalf("mismatch = %+v, want 1500/1000", mismatches[0])
	}
	// Reconcile never auto-fixes.
	if bal, _ := s.Balance(ctx, testUser); bal != 1500 {
		t.Fatalf("balance auto-fixed = %d, want 1500 (report only)", bal)
	}
}
