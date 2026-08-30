package credits

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
}

const schemaVersion = 2

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
	return nil
}

func (s *Service) applyMigration(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err = tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("credits: migration %d: %w", m.version, err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO credits_schema_migrations(version, applied_at) VALUES (?, ?)`, m.version, s.cfg.Now().Unix()); err != nil {
		return fmt.Errorf("credits: migration %d record: %w", m.version, err)
	}
	return tx.Commit()
}

func isMigrationAlreadyApplied(err error) bool {
	return errors.Is(err, sql.ErrNoRows) // kept explicit; normal UNIQUE errors remain fatal
}
