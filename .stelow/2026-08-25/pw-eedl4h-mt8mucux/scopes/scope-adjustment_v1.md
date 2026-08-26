# Scope Adjustment — ai-credits (v1)

> After product gate approval. Source: spec-product_v1 + PLAN.md §10 (stages 0-8).

## Confirmed IN (from spec IN/OUT, unchanged — gate approved as-is)

1. **Ledger + balances** (PLAN §4-5): immutable ledger, materialized balance, `Grant`, `Refund`, `Balance`, idempotency keys, INTEGER timestamps, SQLite `EnsureSchema`.
2. **Pricing engine** (PLAN §8): JSON pricing file + embedded defaults, microunits, `Cost`, `EstimateMax`, `Credits` (ceil), `pricing_version` on usage.
3. **Usage recording** (`RecordUsage`, `Usage` type owned by lib — goai-agnostic).
4. **Reservations** (§6.1-6.3): `Reserve`/`Settle`/`Release`, timeout expiry, 100-goroutine race invariant (20/80), no negative balance path.
5. **Monthly grant + reconcile** (§6.5-6.6): lazy `EnsureMonthlyGrant`, injected clock, `Reconcile` (orphan expiry + mismatch report, no autofix).
6. **BYOK** (§7): XChaCha20-Poly1305 credential store + in-process OpenAI-compatible relay (SSE-safe, `X-Auth-User` identity), gated by env.
7. **Explicit billing mode** per request: `managed` | `byok` | `explicit`.
8. **gogogo integration** (§11): `features/credits/` plugin + routes + config + gateway + test, SCOPE markers, removable.
9. **treinamento integration** (§12): thin `features/credits/`, `chat_send.go` wrapped, routes, test.
10. **Stripe top-up** (§10 Stage 8, app-side, env-gated): checkout + webhook → `Grant(key="stripe:"+payment_intent_id)`; off without keys.

## Confirmed OUT (unchanged)

Scheduler, double-entry, buckets/consumption order, streaming settlement, rate limiting,
non-OpenAI BYOK providers, analytics, in-lib Stripe, HTMX/Templ-heavy UI (optional view only).

## Implementation scopes (map to PLAN §10 stages)

| # | Scope | PLAN §10 | Blocks | Depends on |
|---|-------|----------|--------|-----------|
| s1 | Scaffold (go.mod, LICENSE, AGENTS.md, CI, README stub) | Stage 0 | s2 | — |
| s2 | Schema + ledger (Grant/Refund/Balance, idempotency) | Stage 1 | s3,s4 | s1 |
| s3 | Pricing + usage (Cost/EstimateMax/Credits, RecordUsage) | Stage 2 | s4 | s2 |
| s4 | Reservations (Reserve/Settle/Release, race test) | Stage 3 | s5 | s2,s3 |
| s5 | Monthly grant + reconcile | Stage 4 | s6 | s4 |
| s6 | BYOK (credentials + relay) | Stage 5 | s8 | s1 |
| s7 | gogogo integration `features/credits/` | Stage 6 | s9 | s2,s3,s4,s5,s6 |
| s8 | treinamento integration (chat_send.go) | Stage 7 | — | s7 |
| s9 | Stripe (app-side, env-gated) | Stage 8 | — | s7 |

## Decision

IN/OUT from spec-product_v1 confirmed as-is; no additions/removals (plan is executable
and gate already approved). Proceeding to interface.
