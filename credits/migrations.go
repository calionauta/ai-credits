package credits //nolint:lll,goconst

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Migrations are append-only. Never edit an existing migration after release.
var migrations = []migration{
	{version: 1, sql: schemaV1SQL},
	{version: 2, sql: `CREATE INDEX IF NOT EXISTS idx_credit_reservations_stale ON credit_reservations(status, created_at);`},
	{version: 3, sql: `ALTER TABLE byok_credentials ADD COLUMN version INTEGER NOT NULL DEFAULT 1; ALTER TABLE byok_credentials ADD COLUMN previous_key BLOB; CREATE TABLE IF NOT EXISTS settlement_outbox (request_id TEXT PRIMARY KEY, user_id TEXT NOT NULL, reservation_id TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', attempt_count INTEGER NOT NULL DEFAULT 0, last_error TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL); CREATE INDEX IF NOT EXISTS idx_settlement_outbox_pending ON settlement_outbox(status, created_at);`},
}

const schemaVersion = 3

type migration struct {
	version int
	sql     string
}

func (s *Service) EnsureSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS credits_schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM credits_schema_migrations`).Scan(&current); err != nil {
		return err
	}
	if current > schemaVersion {
		return fmt.Errorf("credits: database schema %d is newer than supported %d", current, schemaVersion)
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			if isMigrationAlreadyApplied(err) {
				continue
			}
			return err
		}
	}
	// Ensure latest schema for fresh DBs: apply schema.sql idempotently after migrations (covers settlement_outbox/backfill).
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("credits: ensure schema: %w", err)
	}
	return nil
}

func (s *Service) applyMigration(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	// Migration 3 contains multiple statements: split.
	if m.version == 3 {
		stmts := []string{
			`ALTER TABLE byok_credentials ADD COLUMN version INTEGER NOT NULL DEFAULT 1`,
			`ALTER TABLE byok_credentials ADD COLUMN previous_key BLOB`,
			`CREATE TABLE IF NOT EXISTS settlement_outbox (request_id TEXT PRIMARY KEY, user_id TEXT NOT NULL, reservation_id TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', attempt_count INTEGER NOT NULL DEFAULT 0, last_error TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
			`CREATE INDEX IF NOT EXISTS idx_settlement_outbox_pending ON settlement_outbox(status, created_at)`,
		}
		for _, st := range stmts {
			if _, err = tx.ExecContext(ctx, st); err != nil {
				// ALTER may fail if column already exists on rerun - ignore.
				if m.version == 3 && isAlreadyExists(err) {
					continue
				}
				return fmt.Errorf("credits: migration %d: %w", m.version, err)
			}
		}
	} else {
		if _, err = tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("credits: migration %d: %w", m.version, err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO credits_schema_migrations(version, applied_at) VALUES (?, ?)`, m.version, s.cfg.Now().Unix()); err != nil {
		return fmt.Errorf("credits: migration %d record: %w", m.version, err)
	}
	return tx.Commit()
}

func isMigrationAlreadyApplied(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "already exists") || contains(msg, "duplicate column")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
