package credits

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestReserveSettleRelease(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	rsv, err := s.Reserve(ctx, testUser, "req-1", 500)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if rsv.Amount != 500 || rsv.Status != reservationStatusReserved {
		t.Fatalf("reservation = %+v, want amount 500 status reserved", rsv)
	}
	bal, _ := s.Balance(ctx, testUser)
	if bal != 500 {
		t.Fatalf("balance after reserve = %d, want 500", bal)
	}

	// Settle at 137 credits; 363 returned.
	if err := s.Settle(ctx, rsv, 137); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	bal, _ = s.Balance(ctx, testUser)
	if bal != 863 {
		t.Fatalf("balance after settle 137 = %d, want 863", bal)
	}
	if rsv.Status != reservationStatusCaptured || rsv.Captured != 137 || rsv.Released != 363 {
		t.Fatalf("settled reservation = %+v", rsv)
	}
}

func TestReserveFullSettle(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 500, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	rsv, _ := s.Reserve(ctx, testUser, "req-1", 500)
	if err := s.Settle(ctx, rsv, 500); err != nil {
		t.Fatalf("full Settle: %v", err)
	}
	bal, _ := s.Balance(ctx, testUser)
	if bal != 0 {
		t.Fatalf("balance after full settle = %d, want 0", bal)
	}
}

func TestRelease(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	rsv, _ := s.Reserve(ctx, testUser, "req-1", 400)
	if err := s.Release(ctx, rsv); err != nil {
		t.Fatalf("Release: %v", err)
	}
	bal, _ := s.Balance(ctx, testUser)
	if bal != 1000 {
		t.Fatalf("balance after release = %d, want 1000", bal)
	}
	if rsv.Status != reservationStatusReleased {
		t.Fatalf("status = %s, want released", rsv.Status)
	}
}

func TestSettleExceedsReservation(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	rsv, _ := s.Reserve(ctx, testUser, "req-1", 100)

	// Actual cost (200) exceeds the reserve (100): the full 200 is captured
	// and the 100 deficit is drawn from available balance (overage). Balance
	// drops by 200 total (100 reserved + 100 overage), never blocking
	// already-consumed work.
	if err := s.Settle(ctx, rsv, 200); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	bal, _ := s.Balance(ctx, testUser)
	if bal != 800 {
		t.Fatalf("balance = %d, want 800 (1000 - 200 actual cost)", bal)
	}
	if rsv.Status != reservationStatusCaptured {
		t.Fatalf("status = %s, want captured", rsv.Status)
	}
	// Invariant: balance == SUM(ledger) must hold after the overage draw.
	if ms, err := s.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	} else if len(ms) != 0 {
		t.Fatalf("drift after overage: %+v", ms)
	}
}

// Overage can push the balance negative; the next Reserve then fail-closes
// (dunning signal) instead of silently allowing debt to run up.
func TestSettleOverageGoesNegative(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 100, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	rsv, _ := s.Reserve(ctx, testUser, "req-1", 100)
	if err := s.Settle(ctx, rsv, 300); err != nil { // over by 200
		t.Fatalf("Settle: %v", err)
	}
	bal, _ := s.Balance(ctx, testUser)
	if bal != -200 {
		t.Fatalf("balance = %d, want -200 (overage drew into negative)", bal)
	}
	// Fail-closed: can't reserve more with a negative balance.
	if _, err := s.Reserve(ctx, testUser, "req-2", 10); !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("Reserve err = %v, want ErrInsufficientCredits", err)
	}
}

func TestSettleOrReleaseClosedReservation(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	rsv, _ := s.Reserve(ctx, testUser, "req-1", 100)
	if err := s.Settle(ctx, rsv, 20); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	// Second settle → ErrReservationClosed. Release too.
	if err := s.Settle(ctx, rsv, 5); !errors.Is(err, ErrReservationClosed) {
		t.Fatalf("second settle err = %v, want ErrReservationClosed", err)
	}
	if err := s.Release(ctx, rsv); !errors.Is(err, ErrReservationClosed) {
		t.Fatalf("release-after-settle err = %v, want ErrReservationClosed", err)
	}
}

func TestReserveIdempotentByRequestID(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	r1, err := s.Reserve(ctx, testUser, "req-1", 500)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	r2, err := s.Reserve(ctx, testUser, "req-1", 500) // same requestID
	if err != nil {
		t.Fatalf("Reserve dup: %v", err)
	}
	if r1.ID != r2.ID || r1.Amount != r2.Amount {
		t.Fatalf("dup reserve not idempotent: %s vs %s", r1.ID, r2.ID)
	}
	// Balance only debited once.
	bal, _ := s.Balance(ctx, testUser)
	if bal != 500 {
		t.Fatalf("balance after dup reserve = %d, want 500", bal)
	}
}

func TestReserveInsufficient(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 100, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	_, err := s.Reserve(ctx, testUser, "req-1", 200) // exceeds balance
	if !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("err = %v, want ErrInsufficientCredits", err)
	}
}

// TestReserveConcurrency is the critical race test: 100 goroutines each try to
// reserve 50 from a balance of 1000. Exactly 20 must succeed; 80 fail with
// ErrInsufficientCredits; final balance is 0 and never negative; invariant holds.
func TestReserveConcurrency(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	const n = 100
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		ok     int
		failed int
	)
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			reqID := fmt.Sprintf("req-%d", i)
			_, err := s.Reserve(ctx, testUser, reqID, 50)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrInsufficientCredits):
				failed++
			default:
				t.Errorf("unexpected err: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if ok != 20 {
		t.Fatalf("successful reserves = %d, want 20", ok)
	}
	if failed != 80 {
		t.Fatalf("insufficient-credit failures = %d, want 80", failed)
	}
	bal, _ := s.Balance(ctx, testUser)
	if bal != 0 {
		t.Fatalf("final balance = %d, want 0", bal)
	}
	// Invariant: balance == SUM(ledger).
	var sum int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount),0) FROM credit_transactions WHERE user_id=?`,
		testUser).Scan(&sum); err != nil {
		t.Fatalf("sum: %v", err)
	}
	if bal != sum {
		t.Fatalf("invariant broken: balance=%d sum=%d", bal, sum)
	}
}

// A second Settle on an already-settled reservation must be refused (CAS on
// status) — otherwise the unused reserve could be refunded twice.
func TestSettleTwiceDoesNotDoubleRefund(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	rsv, err := s.Reserve(ctx, testUser, "req-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(ctx, rsv, 40); err != nil {
		t.Fatalf("first Settle: %v", err)
	}
	bal, _ := s.Balance(ctx, testUser)
	if bal != 960 {
		t.Fatalf("balance after first settle = %d, want 960", bal)
	}
	// Second settle on the same (now captured) reservation must be refused.
	if err := s.Settle(ctx, rsv, 40); !errors.Is(err, ErrReservationClosed) {
		t.Fatalf("second Settle err = %v, want ErrReservationClosed (no double refund)", err)
	}
	if bal2, _ := s.Balance(ctx, testUser); bal2 != 960 {
		t.Fatalf("balance after double settle = %d, want 960 (unchanged)", bal2)
	}
}

// Settle must derive the refund from the DB row's amount, not a tampered
// caller struct (e.g. upsizing r.Amount to steal a bigger refund).
func TestSettleIgnoresTamperedStruct(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	rsv, err := s.Reserve(ctx, testUser, "req-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	// Tamper: claim the reserve was 500 (refund would be 500-40=460 instead
	// of the real 60). The DB row says 100, so only 60 is refunded.
	rsv.Amount = 500
	if err := s.Settle(ctx, rsv, 40); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	bal, _ := s.Balance(ctx, testUser)
	if bal != 960 {
		t.Fatalf("balance = %d, want 960 (refund from DB amount 100, not tampered 500)", bal)
	}
}

func TestGrantRejectsNonPositive(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 0, "signup", "w", "k1"); err == nil {
		t.Fatal("Grant(0) should fail")
	}
	if err := s.Grant(ctx, testUser, -100, "signup", "w", "k2"); err == nil {
		t.Fatal("Grant(-100) should fail")
	}
	if err := s.Refund(ctx, testUser, 0, "admin", "r", "k3"); err == nil {
		t.Fatal("Refund(0) should fail")
	}
	// Balance unchanged by rejected calls.
	if bal, _ := s.Balance(ctx, testUser); bal != 0 {
		t.Fatalf("balance = %d, want 0", bal)
	}
}
