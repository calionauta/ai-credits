package stripe

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// testFileDB opens an isolated file-backed Turso database for one test.
// File-backed, never shared-cache memory: tursogo does not parse the
// modernc `file:NAME?mode=memory&cache=shared` form — it treats the whole
// string as a literal filename (leaving `file:*` files in the package
// dir) and separate opens do not share state. One file per test keeps
// the isolation the shared-cache names used to provide.
func testFileDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("turso", filepath.Join(t.TempDir(), name+".db")+"?_busy_timeout=30000")
	if err != nil {
		t.Fatalf("open test db %s: %v", name, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
