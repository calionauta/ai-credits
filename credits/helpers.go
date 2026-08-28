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
// driver the CALLER compiled in (ncruces/go-sqlite3 → "sqlite3" in gogogo
// and ensaiter; modernc → "sqlite" in this lib's tests). It returns a live
// handle; callers must Close it.
//
// It is the single place the app-side "open a second DB connection for the
// ledger" boilerplate lives — both gogogo and ensaiter used to duplicate it.
// ponytail: pragmas are the conservative safe set for PocketBase-side WAL; if
// an app runs the ledger against a non-PocketBase DB it can still open with
// sql.Open directly.
func OpenSQLite(driverName, path string) (*sql.DB, error) {
	dsn := "file:" + path +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)" +
		"&_pragma=journal_size_limit(200000000)&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-32000)"
	return sql.Open(driverName, dsn)
}
