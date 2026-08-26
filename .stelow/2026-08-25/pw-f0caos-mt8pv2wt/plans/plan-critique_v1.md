# Plan Critique — spec-product_v1.md

**Mode:** plan critique (via stelow)
**Review mode:** Auto → gaps recorded as internal recommendations (no user questions)
**Appetite:** Core | **appetite_fit:** fits

## Method

Checklists applied: Flows, States (domain), Data, System, Feasibility.
UI checklists (Affordances/interaction states) = N/A — this is a backend Go library with no UI.

---

## Flows

- ✅ Primary flow clear: `New(db,cfg)` → `Grant` → `Reserve` → `Settle/Release`. Linear, well-defined.
- ✅ Error paths defined: `ErrInsufficientCredits`, `ErrUnknownModel`, BYOK 404, reservation conflicts.
- ✅ Rollback/implied-undo via `Release` (uncaptured reserved amount returned). Refund is a ledger entry (not a mutation) — immutable ledger simplifies rollback semantics.
- 🔎 Gap: `Settle` after a stream error / cancel path — spec lists `Settle`/`Release` but does not state whether a partial output (usage already consumed by provider) can be settled at a smaller amount than reserved when the client errors mid-stream. Recommendation: allow `Settle(reservation, usage≤reserved)`; document that settle-then-error returns the remaining reserved. (Deferred per §14 streaming partial-settlement, but the ≤reserved settle must be explicit from day 1.)

## Domain States

- ✅ Empty (zero balance, unknown model), Boundary (balance → 0, negative refund allowed), Concurrency (100-goroutine reserve AC #4), Idempotency (replay AC #2, #7).
- ✅ Negative-balance policy explicit (§6.4): grants/refunds may go negative; reserve fails if balance < 0.
- 🔎 `Settle` cap enforcement: AC #3 reserves 500, settles 137, returns 363. Must also define: does `Settle` silently clamp usage>reserved, or error? Recommendation: reject `Settle(usage > reserved)` with a defined error (`ErrReservationExceeded`) to force explicit reservation-topup or override — silent clamp hides misconfig.

## Data

- ✅ Ledger immutability (append-only `credit_transactions`), materialized balance + invariant check (`balance == SUM(ledger)`), idempotency keys UNIQUE.
- ✅ Monthly grant lazy + idempotent (no scheduler race).
- ✅ SQLite schema ownership via `EnsureSchema` (`CREATE TABLE IF NOT EXISTS`; app supplies `*sql.DB`).
- 🔎 Balance stored vs recomputed: spec says "materialized balance" with invariant reconciliation. Confirm write path updates `credits_balances` and ledger in ONE tx (atomic) so `Reconcile` finds mismatches only from non-lib writes, not lib bugs. Recommendation: document single-tx invariant in §4 (not a gap in behavior — PLAN.md §4 already atomic; noted for the implementing agent).

## System (contracts)

- ✅ API contracts explicit (GetBalance, Grant, Reserve, Settle, Release, Cost, Credits, EnsureMonthlyGrant, Reconcile, BYOK CredentialStore + relay).
- ✅ Timeout/orphan handling via `Reconcile` (default 5min, clock-injectable for tests).
- ✅ Encryption: XChaCha20-Poly1305 (libsodium-compatible), provider-gated decryption.
- 🔎 BYOK relay streaming: spec uses `httputil.ReverseProxy` (streams natively) — confirm `FlushInterval`/streaming passthrough and that the requirement "no full-buffer" holds. `ReverseProxy` streams by default; note in scopes so it is not replaced with a buffering client.

## Feasibility / appetite_fit

- ✅ Greenfield, zero-deps beyond `golang.org/x/crypto`, SQLite-only — well within Core appetite. 9 stages (0..5 lib, 6..8 integrations); lib is predominantly tables + transactions + math.
- ✅ Own repo resolves the `internal/` cross-module import rule and MVS conflicts (PLAN §0.1, §0.2) — correct architectural decisions recorded.
- ✅ No real-LLM tests (gogogo rule) honored via httptest fake.
- ⚖️ Appetite fits: Core is appropriate — the lib is state-machine + crypto boilerplate, low discovery risk. Not cuts_needed; not reshape.

---

## Gap Registry (classified)

| # | Sev | Area | Gap | Recommendation | Auto-action |
|---|-----|------|-----|----------------|-------------|
| G1 | 🔎 | Flows/States | `Settle` on mid-stream client error | Allow `Settle(usage ≤ reserved)`; remaining reserved returned via the standard release-on-settle path | Record in spec IN: "unknown-output settle, clamped to reserved" |
| G2 | 🔎 | States | `Settle(usage > reserved)` semantics | Reject with `ErrReservationExceeded` (no silent clamp) | Record as explicit behavior in §6/§10.3 |
| G3 | 🔎 | System | BYOK relay streaming | Keep `ReverseProxy` (streams natively); do not substitute a buffering client | Note in scaffold scope §9 |
| G4 | 🔎 | Data | Ledger+balance atomicity | Single-tx update of ledger + balance; reconcile detects only external drift | Note in §4 for implementing agent |

## AGENTS / lessons

- Name the settle-cap semantics explicitly in the tech spec — it is the single most likely 3am bug (reserve/settle mismatch).
- All gaps are "note" severity; none block the gate. Auto mode resolves them as recorded recommendations in the spec and tech scope.