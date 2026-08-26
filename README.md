# ai-credits

SQLite-backed credits/billing + BYOK for AI applications. Zero deps beyond
`golang.org/x/crypto`.

- Immutable ledger + materialized balance (`balance == SUM(ledger)` invariant)
- JSON-configurable pricing engine (`Cost`, `EstimateMax`, `Credits`)
- Reserve / Settle / Release for unknown-output LLM calls (concurrency-safe)
- Lazy idempotent monthly grants (no scheduler)
- Reconcile (orphan reservation expiry + balance/ledger drift report)
- BYOK: XChaCha20-Poly1305-encrypted credential store + in-process relay

## Install

```bash
go get github.com/calionauta/ai-credits@latest
```

## Example

```go
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/calionauta/ai-credits/credits"
	_ "modernc.org/sqlite" // app supplies the driver
)

func main() {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "app.db")
	if err != nil {
		log.Fatal(err)
	}
	svc, err := credits.New(db, credits.Config{MonthlyCredits: 1000})
	if err != nil {
		log.Fatal(err)
	}

	const user = "user-123"
	// top-up / signup grant (idempotent by key)
	svc.Grant(ctx, user, 1000, "signup", "welcome", "signup:"+user)

	// unknown-output call: reserve a conservative max, settle actual
	max, _ := svc.EstimateMax(ctx, "gpt-4o-mini", 1500, 800)
	rsv, _ := svc.Reserve(ctx, user, "req-1", max)

	// ... run the LLM call, then:
	usage := credits.Usage{Model: "gpt-4o-mini", BillingMode: "managed",
		InputTokens: 1500, OutputTokens: 412}
	cost, _ := svc.Cost(ctx, usage) // micro-units
	if err := svc.Settle(ctx, rsv, cost); err != nil {
		log.Fatal(err)
	}
	svc.RecordUsage(ctx, usage)

	fmt.Println("balance:", must(svc.Balance(ctx, user)))
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
```

BYOK usage, the relay handler, and the app integrations are in the README
sections below (added in later scope stages) and in PLAN.md.

## Docs
- `PLAN.md` — executable implementation plan (architecture, schema, contracts).
- [MIT](./LICENSE)

## Status

Library core (scaffold, schema, ledger, pricing, reservations, monthly,
reconcile, BYOK) — see PLAN.md stages.