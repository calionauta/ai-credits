package credits

import (
	"database/sql"
)

// EstimateTokens is a coarse input-token estimate (4 chars/token) used only
// to size a conservative reserve; the final price always uses real usage on
// settle. Both gogogo and ensaiter previously re-implemented this; the exact
// constant is immaterial (it only inflates the reserve ceiling, corrected by
// the real usage).
func EstimateTokens(s string) int {
	// 4 chars per token is a coarse over-set; settle uses real usage.
	const charsPerToken = 4
	return len(s) / charsPerToken
}

// OpenSQLite opens a SQLite database file via driverName with the connection
// pragmas a billing ledger needs under a second connection to an app DB
// (WAL + busy_timeout + foreign keys + NORMAL sync).
// These match what PocketBase sets on ITS connection so concurrent
// cross-connection writes are safe. driverName is the registered sqlite
// driver the CALLER compiled in ("sqlite3" for ncruces/go-sqlite3,
// "turso" for tursogo — the driver this lib's own tests use). It returns a live
// handle; callers must Close it.
//
// It is the single place the app-side "open a second DB connection for the
// ledger" boilerplate lives — both gogogo and ensaiter used to duplicate it.
// ponytail: pragmas are the conservative safe set for PocketBase-side WAL; if
// an app runs the ledger against a non-PocketBase DB it can still open with
// sql.Open directly.
func OpenSQLite(driverName, path string) (*sql.DB, error) {
	if driverName == "turso" {
		return openTursoLedger(path)
	}
	dsn := "file:" + path +
		"?_txlock=immediate" +
		"&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)" +
		"&_pragma=journal_size_limit(200000000)&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-32000)"
	return sql.Open(driverName, dsn)
}

// openTursoLedger opens the same ledger file through tursogo, which does
// not parse the modernc/ncruces `_txlock`/`_pragma` DSN keys (they poison
// path resolution — "entity not found"). Only `_busy_timeout` survives
// as a DSN option, so the rest applies as post-open PRAGMAs on the
// handle, matching the legacy set above. Callers keep passing "turso"
// as the driverName; nothing else changes.
func openTursoLedger(path string) (*sql.DB, error) {
	// Bare path, not a file: URL: tursogo resolves `file:`-prefixed
	// absolute paths against the wrong base ("entity not found") while
	// the `file:NAME?mode=memory` form stays valid for shared-cache
	// memory databases. Same shape as gogogo's localDSN.
	db, err := sql.Open("turso", path+"?_busy_timeout=10000")
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA journal_size_limit = 200000000",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA temp_store = MEMORY",
		"PRAGMA cache_size = -32000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}
