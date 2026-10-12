# ai-credits

SQLite-backed credits/billing + BYOK for AI applications. A standalone Go
library (zero deps beyond `golang.org/x/crypto`): immutable ledger with
materialized balance, JSON-configurable pricing engine, reserve/settle/release
for unknown-cost LLM calls, lazy idempotent monthly grants, reconcile, and an
in-process BYOK relay with encrypted credentials.

## Commands

```bash
go build ./...          # build
go test -race ./...     # tests (race detector mandatory)
gofumpt -l .            # formatting
golangci-lint run ./... # linters (quality gate, not just CI)
govulncheck ./...       # vulnerability scan
```

## Conventions

- English code; lowercase-kebab-case files.
- No AI attribution in commits/releases.
- No new dependencies without asking — the lib is zero-dep by design
  (`golang.org/x/crypto` is the only exception).
- Timestamps are INTEGER unix (UTC), never TEXT.
- `balance == SUM(ledger)` invariant must hold — never mutate balance outside
  a lib transaction.

## SQLite pragmas (required for concurrency)

Reserve/settle use `BEGIN IMMEDIATE` semantics (write lock up front).
Drivers disagree on what begins immediately, so the guarantee lives in
`immediateTx` (a zero-row `UPDATE` forces the write-lock upgrade up
front), not in DSN dialect. tursogo takes only `_busy_timeout` as a DSN
option — never pass the modernc/ncruces `_txlock`/`_pragma` keys (they
poison path resolution); the rest applies as post-open PRAGMAs (see
`openTursoLedger`). `OpenSQLite("turso", path)` does all of this.
Tests open isolated file-backed DBs via `testFileDB` (never
`file:NAME?mode=memory&cache=shared` — tursogo treats that as a literal
filename). Example:

```go
db, _ := credits.OpenSQLite("turso", "app.db")
db.SetMaxOpenConns(8)
```

## Usage (short)

```go
db, _ := credits.OpenSQLite("turso", "app.db")
svc, _ := credits.New(db, credits.Config{MonthlyCredits: 1000})
svc.Grant(ctx, userID, 1000, "signup", "welcome", "signup:"+userID)
// unknown-output call:
max, _ := svc.EstimateMax(ctx, "gpt-4o-mini", 1500, 800)
rsv, _ := svc.Reserve(ctx, userID, requestID, max)
// ... after the call:
cost, _ := svc.Cost(ctx, usage)
svc.Settle(ctx, rsv, cost)
```

See README.md for the complete example including BYOK.

## DoD

`go test -race ./...` + `golangci-lint run ./...` green; no deps beyond x/crypto.
`govulncheck` last ran with 0 reachable vulns (all 17 findings were Go
stdlib os/exec, crypto/tls, asn1, net/textproto entries from the stale local
go1.26.0 toolchain, none reachable from our code). Bump the toolchain for a
fully clean report.