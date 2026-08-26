# Product Spec — ai-credits (v1)

> Shape Up output. Appetite: **Core**. Source: PLAN.md (executable spec, 2026-08-24).
> Gate artifact kind: `product-spec`.

## Problem

Apps that resell or subsidize LLM usage (the gogogo template, treinamento) have no
metered billing layer: usage is free or fixed, there's no credit system, no way to offer
BYOK to users, and no ledger to reconcile against. Adopting a SaaS metering platform
(OpenMeter, etc.) conflicts with the single-binary Go + SQLite architecture.

## Appetite

**Core.** One small Go library (`ai-credits`) with zero deps beyond `golang.org/x/crypto`,
integrated as a **dependency** into gogogo and treinamento. Not a platform, not a
multi-tenant SaaS. Deliberately no: scheduler, Stripe-in-lib, double-entry, buckets,
streaming settlement, rate limiting, non-OpenAI BYOK providers.

## The pitch (one paragraph)

`ai-credits` is a credit-billing library: an immutable ledger + materialized balance on
SQLite, a pricing engine (microunits per model, versioned), reservations with
capture/release for unknown-cost LLM calls, a lazy idempotent monthly grant, a reconcile
job, and a BYOK credential store (XChaCha20-Poly1305) with an in-process OpenAI-compatible
relay. Apps stay single-binary: the lib takes an app-provided `*sql.DB`, owns its own
tables via `EnsureSchema`, and exposes a small typed Go API (`Grant`, `Reserve`,
`Settle`, `RecordUsage`, `Balance`, …). Billing mode is explicit per request
(`managed` | `byok` | `explicit` default).

## Hill chart

- **Uphill (rabbit holes — the risky/unknown parts)**
  - Reservation concurrency: 100-goroutine race test must land exactly 20/80 success.
  - BYOK crypto round-trip + relay pass-through with SSE (ReverseProxy must not buffer).
  - `balance == SUM(ledger)` invariant under arbitrary sequences (refunds into negative).
- **Downhill (known, mechanical)**
  - Schema, ledger CRUD, pricing math (ceil on microunits), monthly grant, reconcile SQL.

## Rabbit holes (checked, bounded)

1. **SQLite-specific SQL in a driver-agnostic lib** — resolved: lib requires SQLite (schema
   is `CREATE TABLE IF NOT EXISTS` + `ON CONFLICT`); documented, apps pass SQLite only.
2. **Reservation timeouts** — bounded: `ReservationTimeout` config (default 5m), orphan
   expiry inside `Reconcile`. No scheduler.
3. **Refund policy** — decided: refunds may drive balance negative; managed `Reserve`
   blocked when `balance < 0`.
4. **BYOK relay identity** — bounded: app auth middleware sets `X-Auth-User` after session
   validation (app strips external headers); documented in README.
5. **Module graph conflicts** (PocketBase pin 0.39.1 in treinamento vs newer in gogogo) —
   resolved by zero-dep lib: each app maps goai→lib Usage in ~8 lines.

## Key constraints

- Zero deps in lib (only `golang.org/x/crypto`).
- INTEGER unix timestamps (no TZ bugs).
- `idempotency_key UNIQUE` is the backstop; duplicate checks inside transactions.
- Every stage ends green on `go test -race ./...` + `golangci-lint run ./...`.
- Integrations are dependency-based (`go get`), never vendored code.

## Fat / trimmed for v1

- **IN**: ledger, pricing, reservations, monthly grant, reconcile, BYOK (creds+relay),
  explicit billing mode, gogogo plugin `features/credits/`, treinamento thin integration,
  Stripe checkout+webhook in app (env-gated).
- **OUT**: scheduler, double-entry, buckets/consumption order, streaming settlement,
  rate limiting, non-OpenAI BYOK, analytics, in-lib Stripe.

## Definition of Done (PLAN.md §16)

1. `go test -race ./...` + `golangci-lint run ./...` green in lib.
2. `balance == SUM(ledger)` invariant tested after arbitrary sequences.
3. No deps beyond `golang.org/x/crypto`.
4. gogogo `make ci-local` green; SCOPE ok; feature removable by deleting
   `features/credits/` + 2 registration points.
5. treinamento `go test ./...` green; real chat consumes credits.
6. README with 20-line usage example.
