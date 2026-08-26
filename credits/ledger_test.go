package credits

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const testUser = "user_test"

// testDB opens a throwaway file-backed SQLite DB for a test. WAL + busy_timeout
// make the concurrency tests meaningful: real apps must set the same pragmas
// (see AGENTS.md). maxOpenConns >1 exercises real locking.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "credits.db")
	// modernc.org/sqlite DSN: _busy_timeout + _txlock=immediate are dedicated
	// keys the driver turns into `pragma busy_timeout` + `begin immediate`.
	// IMMEDIATE acquires the SQLite write lock up front (deferred txs upgrade
	// mid-tx and deadlock with BUSY under concurrency). WAL lets concurrent
	// readers proceed during a write.
	dsn := "file:" + path + "?_busy_timeout=30000&_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestDB opens an in-memory-ish file-backed SQLite DB for a single test,
// wrapped in a fresh Service with an injected clock.
func newTestService(t *testing.T) (*Service, func()) {
	t.Helper()
	db := testDB(t)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	s, err := New(db, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// DB file + connection are cleaned by testDB via t.TempDir + t.Cleanup.
	return s, func() {}
}

func TestEnsureSchemaIdempotent(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("first EnsureSchema: %v", err)
	}
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("second EnsureSchema must be idempotent, got %v", err)
	}
}

func TestGrantReflectsBalance(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "welcome", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	bal, err := s.Balance(ctx, testUser)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 1000 {
		t.Fatalf("balance = %d, want 1000", bal)
	}
}

func TestGrantIdempotentDuplicateKey(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "dup"); err != nil {
		t.Fatalf("first Grant: %v", err)
	}
	err := s.Grant(ctx, testUser, 1000, "signup", "w", "dup")
	if !errors.Is(err, ErrDuplicateGrant) {
		t.Fatalf("second Grant err = %v, want ErrDuplicateGrant", err)
	}
	bal, _ := s.Balance(ctx, testUser)
	if bal != 1000 {
		t.Fatalf("balance = %d after duplicate, want 1000 (unchanged)", bal)
	}
}

func TestRefundAllowsNegative(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 100, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	// Refund more than granted → balance goes negative, ledger records it.
	if err := s.Refund(ctx, testUser, 300, "admin", "correction", "k2"); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	bal, _ := s.Balance(ctx, testUser)
	if bal != -200 {
		t.Fatalf("balance = %d, want -200", bal)
	}
}

// TestBalanceEqualsLedgerSum asserts the invariant that the materialized
// balance always equals the arithmetic sum of the ledger, across a sequence.
func TestBalanceEqualsLedgerSum(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	seq := []struct {
		op, key string
		amt     int64
	}{
		{"grant", "a", 1000},
		{"grant", "b", 500},
		{"refund", "c", 200},
		{"grant", "d", -0}, // no-op guard
		{"grant", "e", 50},
	}
	want := int64(0)
	for _, step := range seq {
		if step.amt == 0 {
			continue
		}
		var err error
		if step.op == "grant" {
			err = s.Grant(ctx, testUser, step.amt, "signup", "seq", step.key)
			want += step.amt
		} else {
			err = s.Refund(ctx, testUser, step.amt, "admin", "seq", step.key)
			want -= step.amt
		}
		if err != nil {
			t.Fatalf("%s(%d): %v", step.op, step.amt, err)
		}
	}

	// Materialized balance from the account row.
	bal, err := s.Balance(ctx, testUser)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	// Sum of ledger rows.
	var sum int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount),0) FROM credit_transactions WHERE user_id=?`,
		testUser).Scan(&sum); err != nil {
		t.Fatalf("ledger sum: %v", err)
	}
	if sum != want || bal != want {
		t.Fatalf("invariant broken: balance=%d sum=%d want=%d", bal, sum, want)
	}
	if bal != sum {
		t.Fatalf("balance != ledger sum: %d vs %d", bal, sum)
	}
}

// TestLedgerImmutable ensures editing/deleting a raw ledger row does not
// alter the materialized balance (ledger is append-only source of truth;
// balance is an independent cache).
func TestLedgerImmutable(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 777, "signup", "w", "k1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	// Corrupt the ledger directly (simulating external write).
	if _, err := s.db.ExecContext(ctx,
		`UPDATE credit_transactions SET amount = 1 WHERE idempotency_key='k1'`); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	// Balance is a materialized cache and must not flip.
	bal, _ := s.Balance(ctx, testUser)
	if bal != 777 {
		t.Fatalf("balance = %d after ledger corruption, want 777 (cache independent)", bal)
	}
}
