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

// immediateTx begins a write transaction. To get a true BEGIN IMMEDIATE
// (acquiring the SQLite write lock up front, so concurrent writers serialize
// instead of deadlocking with SQLITE_BUSY when a deferred tx upgrades mid-tx)
// the guarantee must come from the DSN, not from an isolation constant:
//   - modernc.org/sqlite: a non-empty DSN `_txlock=` sets beginMode; by itself
//     LevelSerializable does NOT imply BEGIN IMMEDIATE here.
//   - ncruces/go-sqlite3: maps LevelSerializable to "immediate".
//
// OpenSQLite sets `_txlock=immediate`, making the guarantee driver-independent
// for the app-facing entry point. Callers that pass their own *sql.DB must
// ensure their DSN sets _txlock=immediate (or an equivalent) — the isolation
// constant alone is not relied upon here.
func (s *Service) immediateTx(ctx context.Context) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
}
