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

Reserve/settle use `BEGIN IMMEDIATE` semantics (write lock up front). With
`modernc.org/sqlite` the driver maps `_txlock=immediate` and `_busy_timeout`
from the DSN — without them concurrent writers deadlock with SQLITE_BUSY. Set
WAL too. Example:

```go
db, _ := sql.Open("sqlite", "file:app.db?_txlock=immediate&_busy_timeout=30000&_pragma=journal_mode(WAL)")
db.SetMaxOpenConns(8)
```

## Usage (short)

```go
db, _ := sql.Open("sqlite", "app.db")
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