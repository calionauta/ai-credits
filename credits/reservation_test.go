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
	err := s.Settle(ctx, rsv, 200) // > reserved
	if !errors.Is(err, ErrReservationExceeded) {
		t.Fatalf("err = %v, want ErrReservationExceeded", err)
	}
	// Reservation still open after a rejected settle.
	bal, _ := s.Balance(ctx, testUser)
	if bal != 900 {
		t.Fatalf("balance = %d after rejected settle, want 900 (still reserved)", bal)
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
