---
name: ai-credits — créditos/billing + BYOK (lib Go)
intent: new-product
appetite: Core
appetite_source: setup
review_mode: Auto
review_mode_source: setup
appetite_fit: fits
domains_detected: [pricing]
assumptions_resolved:
  - greenfield: confirmed (empty repo, no commits)
  - lib_reusable: confirmed (own repo, not inside gogogo internal/)
  - sqlite_only: confirmed (no redis/postgres)
  - billing_explicit: confirmed (managed | byok | explicit mode per request)
---

# ai-credits — SQLite billing + BYOK library

## Problem

AI-driven products (gogogo, treinamento) consume LLM tokens at variable cost
and need a way to meter, price, reserve, and bill that usage — plus let users
bring their own API keys (BYOK). Today there is no shared, reusable,
dependency-free mechanism; each app would otherwise reinvent billing against
PocketBase or hardcode prices in handlers.

## Solution

A standalone Go module `github.com/calionauta/ai-credits` (single SQLite
database, zero deps beyond `golang.org/x/crypto`) exposed as a library. It
provides:

- **Ledger**: immutable `credit_transactions` + materialized balance, with
  idempotent `Grant`/`Refund` (stable idempotency keys).
- **Pricing engine**: JSON-configurable per-model `/mtok` prices with built-in
  defaults; `Cost`, `EstimateMax`, `Credits` with `microunits_per_credit`
  (1 credit = $0.001).
- **Reservations**: `Reserve` → `Settle`/`Release` for unknown-output-cost LLM
  calls, with concurrency-safe anti-negative guard and idempotency by request.
  `Settle(usage ≤ reserved)` is required; `Settle(usage > reserved)` is rejected
  with `ErrReservationExceeded` (no silent clamp) so misconfigured caps surface
  as errors.
- **Monthly grant**: lazy idempotent `EnsureMonthlyGrant` (no scheduler).
- **Reconcile**: balance-vs-ledger mismatch report + orphan reservation expiry.
- **BYOK**: `CredentialStore` (XChaCha20-Poly1305 encrypted keys, provider
  gated) + in-process `http.Handler` relay (pass-through `httputil.ReverseProxy`
  with key injection).
- **Schema ownership**: idempotent `EnsureSchema` (lib owns its tables via
  `CREATE TABLE IF NOT EXISTS`, app supplies `*sql.DB`).

It enters consumer apps as a **dependency** (plugin `features/credits/` in
gogogo; thin wrapper + `chat_send.go` integration in treinamento), with Stripe
top-up as an app-side, env-gated option.

## IN scope

- Go library `ai-credits` with the contracts in PLAN.md §3.
- SQLite schema §4, pricing engine §5, domain rules §6, BYOK §7, security §8.
- Scaffold §9: go.mod, LICENSE (MIT), AGENTS.md, CI (`go test -race`, golangci,
  govulncheck), README stub.
- Implemented in stages §10 (0..5 = lib; 6..8 = app integrations).
- gogogo integration as plugin `features/credits/` (dependency, SCOPE-marked).
- treinamento integration (thin wrapper + chat_send.go).
- Stripe top-up in apps, gated by env (optional).
- Tests: pricing tables, ledger idempotency, reservation concurrency (-race),
  BYOK crypto round-trip + relay with httptest fake. No real LLM calls.

## OUT scope

- Stripe inside the lib (app-only).
- Double-entry accounting, multiple wallets, transfers.
- Consumption buckets with ordering (single pool; §14 deferred).
- Streaming with partial settlement (usage recorded at end of stream).
- OpenMeter/ClickHouse/Kafka/Redis/Postgres.
- Internal scheduler (monthly grant is lazy; reconcile is app-called).
- Rate limiting in BYOK relay (v1; app decides).
- Non-OpenAI BYOK provider adapters (Anthropic/Gemini native formats).

## Risks / Rabbit holes

- Mismatch of `goai` usage fields across gogogo v0.9.6 vs treinamento v0.7.6 →
  lib defines its own `Usage`, apps map in ~8 lines.
- Go module rule: `internal/` cannot be imported cross-module → lib lives in
  its own repo (decided §0.1), never in gogogo `internal/`.
- MVS dependency conflicts if lib lived in gogogo `pkg/` → own module with
  zero deps avoids it (decided §0.2).
- SQLite concurrency on reservations → single-transaction UPDATE with
  `balance >= amount` guard; `ON CONFLICT(request_id) DO NOTHING` for
  idempotency backstop.
- Refund policy (decided §6.4): Grants/Refunds allow negative balance; Reserve
  fails if `balance < 0`.
- Driver choice agnostic: lib receives `*sql.DB`; no driver import in lib.
- Reservation orphan/timeout via `Reconcile` (default 5min, clock-injectable).

## Definition of Done (Product)

- [ ] `go test -race ./...` green on the lib; invariant `balance == SUM(ledger)`
      holds after arbitrary sequences.
- [ ] Consumer gogogo: `make ci-local` green; SCOPE ok; feature removable by
      deleting `features/credits/` + 2 registration points.
- [ ] Consumer treinamento: `go test ./...` green; real chat debits a credit
      (visible in `llm_usage`).
- [ ] README with a ~20-line usage example (New + Grant + Reserve/Settle + BYOK
      mount).
- [ ] No real-LLM tests (gogogo rule).

## Acceptance Criteria

1. Given a fresh DB, when `New(db, cfg)` runs, then schema is created and
   `EnsureSchema` is idempotent.
2. Given `Grant(user, 1000, "signup", ...)` twice with the same idempotency
   key, then only one transaction is recorded and balance is 1000.
3. Given `Reserve(user, req, 500)`, `Settle(reservation, 137)`, then 137 is
   captured, 363 returned, balance restored to start-137.
4. Given 100 goroutines calling `Reserve(50)` on balance 1000, then exactly 20
   succeed and 80 get `ErrInsufficientCredits`; balance ends at 0, never
   negative (run with `-race`).
5. Given an unknown pricing model, `Cost` returns `ErrUnknownModel`.
6. Given a BYOK relay request without a stored credential, it returns 404.
7. Given a Stripe webhook replay, the grant does not double (`idempotency_key
   UNIQUE`); without `STRIPE_SECRET_KEY` the feature is off and app runs
   without billing.