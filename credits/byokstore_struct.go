package credits

import (
	"database/sql"
)

// CredentialStore seals user BYOK credentials at rest. Zero value is disabled
// (no sealing key configured). Credentials are only ever returned server-side
// via Get, for the in-process relay.
type CredentialStore struct {
	db   *sql.DB
	blob [32]byte
	now  func() int64
}
