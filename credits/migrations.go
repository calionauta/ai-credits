package credits

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaVersion is bumped only when a new migration is appended below.
const schemaVersion = 1

type migration struct {
	version int
	sql     string
}

var migrations = []migration{{version: 1, sql: schemaSQL}}

// EnsureSchema applies ordered, durable schema migrations. Existing databases
// created before migrations are adopted are treated as version 0 and receive
// the idempotent base schema once.
func (s *Service) EnsureSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS credits_schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM credits_schema_migrations`).Scan(&current); err != nil {
		return err
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyMigration(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err = tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("credits: migration %d: %w", m.version, err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO credits_schema_migrations(version, applied_at) VALUES (?, ?)`, m.version, s.cfg.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
