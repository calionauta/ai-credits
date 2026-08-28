# ai-credits

SQLite-backed credits/billing + BYOK for AI applications. Zero deps beyond
`golang.org/x/crypto`.

- Immutable ledger + materialized balance (`balance == SUM(ledger)` invariant)
- JSON-configurable pricing engine (`Cost`, `EstimateMax`, `Credits`)
- Reserve / Settle / Release for unknown-output LLM calls (concurrency-safe)
- Lazy idempotent monthly grants (no scheduler)
- Subscription lifecycle: plan status (active/paused/cancelled) gates the
  monthly grant (entitlement), `SetSubscription`/`CancelSubscription`
- BYOK: XChaCha20-Poly1305-encrypted credential store + in-process relay
  that auto-meters every upstream call into `llm_usage` (billing_mode=byok)
- Reconcile (orphan reservation expiry + balance/ledger drift report)

## Install

```bash
go get github.com/calionauta/ai-credits@latest
```

## Example

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/calionauta/ai-credits/credits"
	_ "modernc.org/sqlite" // app supplies the driver
)

func main() {
	ctx := context.Background()
	// OpenSQLite applies the WAL + busy_timeout pragmas the concurrency-safe
	// ledger needs; a bare sql.Open can deadlock on concurrent writes.
	db, err := credits.OpenSQLite("sqlite", "app.db")
	if err != nil {
		log.Fatal(err)
	}
	svc, err := credits.New(db, credits.Config{MonthlyCredits: 1000})
	if err != nil {
		log.Fatal(err)
	}

	const user = "user-123"
	// top-up / signup grant (idempotent by key)
	if err := svc.Grant(ctx, user, 1000, "signup", "welcome", "signup:"+user); err != nil {
		log.Fatal(err)
	}

	// unknown-output call: reserve a conservative max, settle actual
	max, _ := svc.EstimateMax(ctx, "gpt-4o-mini", 1500, 800)
	rsv, _ := svc.Reserve(ctx, user, "req-1", max)

	// ... run the LLM call, then:
	usage := credits.Usage{Model: "gpt-4o-mini", BillingMode: "managed",
		InputTokens: 1500, OutputTokens: 412}
	// Settle takes CREDITS (not micro-units). Convert the cost to credits
	// first — passing micro-units would look like a huge over-charge and
	// Settle returns ErrReservationExceeded.
	creditsUsed, _ := svc.Credits(ctx, usage)
	if err := svc.Settle(ctx, rsv, creditsUsed); err != nil {
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

## BYOK (bring your own key)

For apps where each user brings their own LLM provider key, the library ships a
credential store plus an in-process relay that meters every upstream call.

1. **Configure the encryption key** the relay uses to store keys at rest:
   `CREDITS_ENC_KEY` (hex of 32 bytes, e.g. `openssl rand -hex 32`), passed to
   `NewCredentialStore`. No key → the store is disabled (clear error).
2. **Define provider bases** — a `map[string]string` of provider →
   OpenAI-compatible base URL (e.g. `openai: https://api.openai.com/v1`).
3. **Mount the relay** behind your auth middleware (the relay does not
   authenticate; it trusts `X-Auth-User`, which the app must set only after
   session validation and strip from external requests):

```go
store := svc.NewCredentialStore(key32)          // key32 [32]byte
relay := svc.NewByokRelay(store, providerBases, logger)
mux.Handle("/api/byok/", authMiddleware(relay)) // POST /api/byok/{provider}/{path...}
```

Each call is proxied OpenAI-compat to the user's provider with their key injected
as the bearer token, and the upstream `usage` (JSON or final SSE chunk) is
captured into `llm_usage` with `billing_mode=byok` and `credits_charged=0` — so
BYOK calls are visible to analytics/throttling without charging the user.

## Architecture

Design reference (schema, pricing, domain rules, BYOK relay, security,
deliberate simplifications): [`docs/architecture.md`](docs/architecture.md).

- [MIT](./LICENSE)