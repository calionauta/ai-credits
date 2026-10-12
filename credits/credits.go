package credits

import (
	"context"
	"database/sql"
	"io"
	"time"
)

// Config holds all tunable knobs for a credits Service.
type Config struct {
	DefaultBillingMode string           // "managed" | "byok" | "explicit" (default "explicit")
	MonthlyCredits     int64            // default monthly grant (0 = disabled)
	PlanMonthlyCredits map[string]int64 // plan -> monthly grant (optional)
	ReservationTimeout time.Duration    // default 5 * time.Minute
	PricingReader      io.Reader        // JSON prices; nil = built-in defaults
	PricingVersion     string           // default "builtin-2026-08"
	Now                func() time.Time // injectable for tests; default time.Now
}

// Service is the credits engine: an immutable ledger over a materialized
// balance, plus pricing, reservations, monthly grants, reconcile, and usage.
type Service struct {
	db     *sql.DB
	cfg    Config
	pricer *pricerEngine
}

// New initializes a credits Service on the given *sql.DB and applies the
// schema idempotently.
func New(db *sql.DB, cfg Config) (*Service, error) {
	if cfg.DefaultBillingMode == "" {
		cfg.DefaultBillingMode = "explicit"
	}
	if cfg.ReservationTimeout <= 0 {
		cfg.ReservationTimeout = 5 * time.Minute
	}
	if cfg.PricingVersion == "" {
		cfg.PricingVersion = "builtin-2026-08"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Service{db: db, cfg: cfg}
	var err error
	s.pricer, err = newPricer(cfg.PricingReader, cfg.PricingVersion)
	if err != nil {
		return nil, err
	}
	if err := s.EnsureSchema(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// immediateTx begins a write transaction that holds the SQLite write
// lock from the start, so concurrent writers serialize instead of
// deadlocking when a deferred tx upgrades mid-tx (SQLITE_BUSY_SNAPSHOT
// at commit, which no busy_timeout retries).
//
// Drivers disagree on what begins immediately, so the guarantee lives
// HERE, not in DSN dialect:
//   - modernc.org/sqlite: DSN `_txlock=immediate` sets beginMode;
//     LevelSerializable alone does NOT imply BEGIN IMMEDIATE.
//   - ncruces/go-sqlite3: maps LevelSerializable to "immediate".
//   - tursogo: honors neither — every tx begins DEFERRED.
//
// Hence the zero-row upgrade below: the first WRITE statement in a
// deferred tx forces the SHARED→RESERVED upgrade up front, acquiring
// the write lock before any business read observes shared state.
// Contenders then meet plain SQLITE_BUSY at upgrade time (which
// busy_timeout retries inside the engine) instead of a stale snapshot
// at commit time (which nothing retries). On drivers that already
// begin immediate the extra statement matches zero rows and is a
// harmless no-op — behavior there is unchanged. All write paths go
// through here, so the one place covers Reserve, Settle, Release and
// Reconcile on every driver.
func (s *Service) immediateTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	// Force the write-lock upgrade now. credit_accounts exists before any
	// write path runs (EnsureSchema precedes every Service method that
	// transacts), and matching zero rows still acquires RESERVED.
	if _, err := tx.ExecContext(ctx,
		`UPDATE credit_accounts SET updated_at = updated_at WHERE 1 = 0`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}
