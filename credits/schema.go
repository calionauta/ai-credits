package credits

import (
	"context"

	_ "embed"
)

//go:embed schema.sql
var schemaSQL string

// EnsureSchema applies the lib-owned schema idempotently via
// CREATE TABLE IF NOT EXISTS. The app supplies the *sql.DB; the lib is
// driver-agnostic (SQLite recommended). Safe to call on every boot.
func (s *Service) EnsureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schemaSQL)
	return err
}
