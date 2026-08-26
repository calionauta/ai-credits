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

// immediateTx begins a SERIALIZABLE transaction, which modernc.org/sqlite maps
// to BEGIN IMMEDIATE — acquiring the SQLite write lock up front. Deferred
// transactions upgrade to write mid-tx and cause SQLITE_BUSY deadlocks under
// concurrency; immediate serializes writers on the lock. Every write path in
// this package uses a write transaction through this helper.
func (s *Service) immediateTx(ctx context.Context) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
}
