---
wf: pw-f0caos-mt8pv2wt
name: ai-credits — lib SQLite billing + BYOK
intent: new-product
appetite: Core
review_mode: Auto
product_type: software
status: approved
source: interactions selected-interface (Hybrid A+E)
---

# Tech Plan — ai-credits lib (Go, SQLite, zero deps)

## 0. Product Context

Greenfield standalone Go module `github.com/calionauta/ai-credits` (single
SQLite DB, zero deps beyond `golang.org/x/crypto`) offering: immutable ledger +
materialized balance, pricing engine (JSON config), reserve/settle/release for
unknown-output LLM calls, lazy idempotent monthly grant, reconcile, and BYOK
(encrypted credential store + in-process relay). Consumers (gogogo plugin
`features/credits/`, treinamento) consume it as a dependency via `go get`.

**Interface (selected Hybrid A+E):** `*credits.Service` facade (typed, own-tx by
default) + explicit-tx escape hatch (`ReserveWithTx`/`SettleWithTx`). See
`interfaces/selected-interface.md`.

**Security:** idempotency_key UNIQUE backstop; single-tx atomic updates of
ledger+balance; XChaCha20-Poly1305 for BYOK keys (32B key, 24B nonce); relay
behind app auth; never log prompts/keys. `govulncheck` in CI.

**Stack:** Go 1.26, SQLite via app-supplied `*sql.DB` (lib driver-agnostic),
`golang.org/x/crypto` only.

## 1. Identified Scopes

| # | Scope | Type | Maps to PLAN |
|---|-------|------|--------------|
| 1 | Scaffold + Schema + Ledger | feature | §9, §4, Stage 0–1 |
| 2 | Pricing + Usage | feature | §5, Stage 2 |
| 3 | Reservations | feature | §6.1–6.3, Stage 3 |
| 4 | Monthly + Reconcile | feature | §6.5–6.6, Stage 4 |
| 5 | BYOK (credentials + relay) | feature | §7, Stage 5 |
| 6 | gogogo plugin integration | feature | §11, Stage 6 |
| 7 | treinamento integration | feature | §12, Stage 7 |
| 8 | Stripe top-up (apps) | feature | §8, Stage 8 |

## 2. High-Level Sequence

Zero-deps core first (1→4 builds the ledger/domain with tests), then BYOK (5),
then app integrations (6, 7) as the lib reaches `go get`-able state, Stripe (8)
last (env-gated, app-only). Sequencing: risk-front-load the SQL concurrency
(scope 3 is highest risk → own iterations); tests are embedded per scope
(Stage-level ACs from PLAN §10, §13).

## 3. Detailed Development Sequence per Scope

### Scope 1 — Scaffold + Schema + Ledger

- **Type:** feature
- **Risk:** 2
- **Goal:** Standalone module with idempotent schema, `Service`/`Config`/`New`,
  and immutable ledger (Grant/Refund/Balance).
- **DoD:** `go build ./...` green; `EnsureSchema` idempotent; grant→balance;
  idempotent double-grant no-ops; refund allows negative; invariant
  `balance == SUM(ledger)` holds; `go test -race` + `golangci-lint` green.
- **Dependencies:** none.
- **[LAYERS]**
  - domain: balance is materialized cache; ledger is source of truth; single-tx update
  - data: `credit_accounts`, `credit_transactions` + indexes
  - api: `New`, `EnsureSchema`, `Balance`, `Grant`, `Refund`
  - ui: n/a
- **[INTEGRATION-POINTS]** outbound: app-supplied `*sql.DB`

| # | Task | Components | Risk | Done Criterion | Order Rationale |
|---|------|-----------|------|---------------|-----------------|
| 1.1 | Repo scaffold: go.mod (go 1.26, x/crypto), LICENSE MIT, AGENTS.md, .golangci.yml, CI.yml, README stub | repo, ci | LOW(2) | `go build ./...` green | P0: foundation for all compile |
| 1.2 | `schema.sql` + `schema.go` `EnsureSchema` (embed, CREATE TABLE IF NOT EXISTS, INTEGER unix timestamps) | db-schema | LOW(2) | idempotent; tables match §4 | P1: schema before domain writes |
| 1.3 | `credits.go` (`Config`, `New`), `types.go` (errors, Usage, Result) | api | LOW(2) | compile; contracts §3 | P2: needs schema |
| 1.4 | `ledger.go` Grant/Refund/Balance (single-tx, idempotency UNIQUE) | db-ledger, service | MED(3) | Stage-1 tests §10 | P0: ledger is core domain |
| 1.5 | Ledger tests: idempotency, negative refund, balance==SUM invariant | test-unit | MED(3) | AC #2; invariant holds | last: validates 1.2–1.4 |

### Scope 2 — Pricing + Usage

- **Type:** feature
- **Risk:** 2
- **Goal:** JSON-configurable pricing engine (defaults built-in), `Cost`,
  `EstimateMax`, `Credits`, and `RecordUsage`.
- **DoD:** tables per §5.2 exact; unknown model → `ErrUnknownModel`;
  `EstimateMax ≥ Cost` invariant; `pricing_version` recorded; `usage.go`
  writes `llm_usage`.
- **Dependencies:** 1 (schema `llm_usage`).

| # | Task | Components | Risk | Done Criterion | Order Rationale |
|---|------|-----------|------|---------------|-----------------|
| 2.1 | `pricing.go` Engine: Load(defaults/JSON), Cost, EstimateMax, Credits, microunits_per_credit | domain-pricing | MED(3) | §5.1 §5.2 exact | P0: core math |
| 2.2 | `usage.go` RecordUsage → llm_usage row | db-usage | LOW(2) | §3 Usage fields persisted | P2: needs schema + pricing |
| 2.3 | Pricing tests: exact cost, ceil, unknown model, EstimateMax≥Cost | test-unit | MED(3) | Stage-2 ACs | last: validates formulas |

### Scope 3 — Reservations

- **Type:** feature
- **Risk:** 4 (SQLite concurrency)
- **Goal:** `Reserve`/`Settle`/`Release` per §6.1–6.3, concurrency-safe,
  idempotent by requestID, with the critique-gated settle semantics.
- **DoD:** 500/137/363 canonical; 100-goroutine `-race` test → exactly 20 succeed
  / 80 `ErrInsufficientCredits`, balance never negative; duplicate requestID →
  same reservation; `Settle(usage>reserved)` → `ErrReservationClosed`;
  `Settle`/`Release` after close → `ErrReservationClosed`.
- **Dependencies:** 1 (schema), 2 (EstimateMax feed).
- **[LAYERS]**
  - domain: reserve/capture/release state machine; anti-negative guard
  - data: `credit_reservations`, reservation-type ledger tx
  - api: `Reserve`, `Settle`, `Release`
- **[INTEGRATION-POINTS]** inbound: unknown-cost LLM callers

| # | Task | Components | Risk | Done Criterion | Order Rationale |
|---|------|-----------|------|---------------|-----------------|
| 3.1 | `reservation.go` Reserve (single-tx, ON CONFLICT(request_id), balance>=amount guard) | db-reserve, service | HIGH(4) | §6.1 exact | P3: risk front-load |
| 3.2 | Settle (cap at reserved, release excess) + Release | db-reserve, service | HIGH(4) | §6.2–6.3; excess returned | P3: risk front-load |
| 3.3 | Concurrency test (100 goroutines, `-race`) + closed/duplicate/idempotency tests | test-unit | HIGH(4) | AC #4 | last: validates 3.1–3.2 |

### Scope 4 — Monthly + Reconcile

- **Type:** feature
- **Risk:** 2
- **Goal:** Lazy idempotent `EnsureMonthlyGrant` (no scheduler) + `Reconcile`
  (orphan expiry + balance/ledger mismatch report).
- **DoD:** same period → single grant; different periods → distinct; stale
  reservation expired + balance returned; mismatch reported (never auto-fixed);
  clock injection (`cfg.Now`) used in all time tests.
- **Dependencies:** 1.

| # | Task | Components | Risk | Done Criterion | Order Rationale |
|---|------|-----------|------|---------------|-----------------|
| 4.1 | `monthly.go` EnsureMonthlyGrant (lazy, cumulative key) | service | LOW(2) | §6.6 | P1: after ledger |
| 4.2 | `reconcile.go` Reconcile (expiry UPDATE + SUM vs balance report) | db-reconcile, service | MED(3) | §6.5; report only | P2: after reservations |
| 4.3 | Monthly + reconcile tests with injected clock | test-unit | MED(3) | Stage-4 ACs | last |

### Scope 5 — BYOK (credentials + relay)

- **Type:** feature
- **Risk:** 3 (crypto)
- **Goal:** `CredentialStore` (XChaCha20-Poly1305) + in-process relay
  (`httputil.ReverseProxy`, pass-through streaming, key injection).
- **DoD:** Put/Get round-trip; wrong key → decrypt error; relay forwards
  `Authorization: Bearer <user key>` with intact body; provider without
  credential → 404; no keys in logs/responses.
- **Dependencies:** 1 (schema).

| # | Task | Components | Risk | Done Criterion | Order Rationale |
|---|------|-----------|------|---------------|-----------------|
| 5.1 | `credentials.go` XChaCha20-Poly1305 store (32B key, 24B nonce) | crypto, db | MED(3) | round-trip; decrypt-error | P0: crypto core |
| 5.2 | `relay.go` ReverseProxy pass-through with key injection, X-Byok strip | api | MED(3) | streaming; 404 path | P3: risk front-load |
| 5.3 | BYOK tests: crypto round-trip, httptest fake relay (SSE), 404 | test-unit | MED(3) | Stage-5 ACs | last |

### Scope 6 — gogogo plugin integration

- **Type:** feature
- **Risk:** 3
- **Goal:** Add lib as dependency; `features/credits/` plugin (New, gateway,
  routes), SCOPE-marked, env-gated; relay behind auth.
- **DoD:** `make ci-local` green; SCOPE ok; removable by deleting
  `features/credits/` + 2 registration points; gateway test with
  `internal/llm/fakeserver`.
- **Dependencies:** 1–5 (lib reachable), external gogogo repo.
- **[INTEGRATION-POINTS]** inbound: router.Init, auth middleware; outbound:
  PocketBase `app.DB().DB()`, internal/llm

| # | Task | Components | Risk | Done Criterion | Order Rationale |
|---|------|-----------|------|---------------|-----------------|
| 6.1 | go.mod dep + `credits.go` New/EnsureSchema/NewCredentialStore | repo, config | MED(3) | lib wired at boot | P0: dependency first |
| 6.2 | `gateway.go` RunManaged (grant→EstimateMax→Reserve→llm→Cost→Settle→usage) + goai mapper | service | MED(3) | §11 flow | P2: uses scopes 1–5 |
| 6.3 | `routes.go` + router/credits.go (SCOPE), credits_test.go with fakeserver | api | MED(3) | §11 routes; test green | P3 |
| 6.4 | Optional views.templ (balance/transactions/BYOK) | ui | LOW(2) | [suggested] optional v1 | P5 nice-to-have |

### Scope 7 — treinamento integration

- **Type:** feature
- **Risk:** 3
- **Goal:** Lib as dependency; thin `features/credits/`; `chat_send.go` wrapped
  with RunManaged/BYOK; relay behind auth.
- **DoD:** `go test ./...` green; real chat debits a credit (visible in
  `llm_usage`).
- **Dependencies:** 1–5, external treinamento repo, §12.

| # | Task | Components | Risk | Done Criterion | Order Rationale |
|---|------|-----------|------|---------------|-----------------|
| 7.1 | go.mod dep + thin credits.go + EnsureSchema boot | repo, config | MED(3) | wired at cmd/web boot | P0 |
| 7.2 | `gateway.go` Runner over existing goai helpers + chat_send.go integration | service | MED(3) | chat debits credit | P2 |
| 7.3 | Routes `/api/credits` + relay; chat_gateway test with LLM stub | api, test-unit | MED(3) | §12 test green | P3 |

### Scope 8 — Stripe top-up (apps)

- **Type:** feature
- **Risk:** 2
- **Goal:** Env-gated Stripe checkout + webhook → `Grant(key="stripe:"+pi)` in
  both apps.
- **DoD:** webhook replay → single grant (idempotency_key UNIQUE); no
  `STRIPE_SECRET_KEY` → feature off, app runs without billing.
- **Dependencies:** 1, apps (6/7).

| # | Task | Components | Risk | Done Criterion | Order Rationale |
|---|------|-----------|------|---------------|-----------------|
| 8.1 | `stripe.go` checkout + webhook (env-gated) in both apps | api, outbound | MED(3) | AC #7 | P1: needs ledger + webhook |
| 8.2 | Webhook-replay idempotency test | test-unit | LOW(2) | single grant | last |

## 4. Non-Functional Requirements (all scopes)

```yaml
nfr:
  error_handling: "typed errors per §3; no silent clamps (settle cap → ErrReservationClosed)"
  observability: "slog-compatible; never log prompts or BYOK keys"
  security: "idempotency_key UNIQUE; single-tx atomic writes; XChaCha20-Poly1305; relay behind auth; govulncheck CI"
  currency: "INTEGER unix timestamps (no TZ bugs); credits int64; microunits int64"
  concurrency: "single-tx anti-negative guard; SQLite via app driver; -race in CI"
  cleanliness: "≤500 lines/file; golangci-lint gate; SCOPE in every gogogo file"
```

## 5. Summary — Main Functional Scopes

1. Scaffold + Schema + Ledger
2. Pricing + Usage
3. Reservations
4. Monthly + Reconcile
5. BYOK
6. gogogo plugin integration
7. treinamento integration
8. Stripe top-up (apps)

## 6. DoD (project-level, per §16)

- `go test -race ./...` + `golangci-lint run ./...` green on lib.
- `balance == SUM(ledger)` invariant tested after arbitrary sequences.
- Zero deps beyond `golang.org/x/crypto`.
- gogogo `make ci-local` green; SCOPE ok; feature removable.
- treinamento `go test ./...` green; real chat consumes credits.
- README 20-line usage example.