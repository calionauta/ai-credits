# ai-credits — Architecture

Durable design reference for the `credit` library. Live implementation is the
source of truth; this document captures the invariants and contracts the code
is built around. It is not an implementation plan — stage-by-stage history lives
in `CHANGELOG.md` and git history.

## Core model

- **Immutable ledger** — `credit_transactions` is the source of truth;
  `credit_accounts.balance` is a cached materialization. The invariant
  `balance == SUM(ledger)` is enforced inside a library transaction — the
  balance is never mutated outside a lib tx.
- **Integer units everywhere** — amounts are integer credits; `Cost` returns
  integer micro-units. No floats in money paths.
- **Idempotency** — `credit_transactions.idempotency_key` is `UNIQUE`; the
  duplicate check happens *inside* the write transaction (never
  check-then-insert outside a tx). `credit_reservations.request_id` is unique.

## Schema

```sql
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS credit_accounts (
    user_id    TEXT PRIMARY KEY,
    balance    INTEGER NOT NULL DEFAULT 0,   -- materialized; SUM(ledger) invariant
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS credit_transactions (
    id              TEXT PRIMARY KEY,          -- hex crypto/rand 16 bytes
    user_id         TEXT NOT NULL,
    amount          INTEGER NOT NULL,          -- +/- credits
    type            TEXT NOT NULL,             -- grant|monthly|topup|refund|reservation|reservation_release|reservation_overage|adjustment
    source          TEXT NOT NULL,             -- signup|admin|stripe|monthly|llm_request|reconcile
    reference_id    TEXT,                      -- reservation_id | payment_intent_id | request_id
    idempotency_key TEXT UNIQUE,               -- e.g. "stripe:pi_123", "monthly:u1:2026-08"
    metadata        TEXT,                      -- optional JSON
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_credit_tx_user ON credit_transactions(user_id, created_at);

CREATE TABLE IF NOT EXISTS credit_reservations (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    request_id      TEXT UNIQUE NOT NULL,      -- reserve idempotency
    amount          INTEGER NOT NULL,          -- reserved credits (= estimate max)
    captured_amount INTEGER NOT NULL DEFAULT 0,
    released_amount INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL,             -- reserved|captured|released|expired
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS llm_usage (
    id                        TEXT PRIMARY KEY,
    request_id                TEXT UNIQUE NOT NULL,
    user_id                   TEXT NOT NULL,
    provider                  TEXT NOT NULL,
    model                     TEXT NOT NULL,
    billing_mode              TEXT NOT NULL,   -- managed|byok
    input_tokens              INTEGER NOT NULL DEFAULT 0,
    output_tokens             INTEGER NOT NULL DEFAULT 0,
    cached_input_tokens       INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens          INTEGER NOT NULL DEFAULT 0,
    estimated_cost_microunits INTEGER,
    actual_cost_microunits    INTEGER,
    credits_charged           INTEGER NOT NULL DEFAULT 0,
    pricing_version           TEXT NOT NULL,
    created_at                INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_llm_usage_user ON llm_usage(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_llm_usage_model ON llm_usage(model);

CREATE TABLE IF NOT EXISTS byok_credentials (
    user_id       TEXT NOT NULL,
    provider      TEXT NOT NULL,               -- key of the provider->base map
    encrypted_key BLOB NOT NULL,               -- nonce || XChaCha20-Poly1305(apiKey)
    version       INTEGER NOT NULL DEFAULT 1,  -- key rotation version (migration v3)
    previous_key  BLOB,                        -- last rotated key, for in-flight lookups
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (user_id, provider)
);

CREATE TABLE IF NOT EXISTS subscriptions (
    user_id    TEXT PRIMARY KEY,
    plan       TEXT NOT NULL,                  -- key used in PlanMonthlyCredits
    status     TEXT NOT NULL,                  -- active|cancelled|paused
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS settlement_outbox (   -- migration v3
    request_id     TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL,
    reservation_id TEXT NOT NULL,
    provider       TEXT NOT NULL,
    model          TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending',  -- pending|settled|failed|expired
    attempt_count  INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
```

- Timestamps are integer unix. The lib writes `time.Now().Unix()` (or `cfg.Now`).
- `credit_accounts` is the materialized cache; `credit_transactions` is truth.
- Extra indexes added by migrations: `idx_credit_reservations_stale` (v2),
  `idx_settlement_outbox_pending` (v3). The schema auto-versions via
  `credits_schema_migrations`; a fresh DB ends at the same final shape
  (fresh DDL + v3 migration both applied).

## Pricing

Prices are configured by a JSON file (`CREDITS_PRICING_FILE` env) or built-in
defaults. Units are **micro-dollars per 1M tokens** (`*_per_mtok`).

```json
{
  "version": "2026-08",
  "microunits_per_credit": 1000,
  "models": {
    "gpt-4o-mini":  { "input_per_mtok": 150000, "output_per_mtok": 600000, "cached_input_per_mtok": 150000, "reasoning_per_mtok": 0 }
  }
}
```

- `microunits_per_credit = 1000` ⇒ **1 credit = $0.001**.
- Built-in defaults: gpt-4o-mini, gpt-4o, claude-3-5-haiku, claude-3-5-sonnet
  (public 2026-08 prices; the app can override via file).

Formulas:

```
cost_microunits = in/1e6*input_per_mtok + out/1e6*output_per_mtok
                + cached/1e6*cached_input_per_mtok + reasoning/1e6*reasoning_per_mtok
credits = ceil(cost_microunits / microunits_per_credit)
```

- `Cost(ctx, u)` → micro-units (ceil at the end).
- `EstimateMax(model, inputTokens, maxOutputTokens)` → conservative credits:
  `credits(input + maxOutput, ignoring cache)` × `RESERVE_MARGIN` (1.2).
- Unknown model → `ErrUnknownModel` (never silent; the app treats it as config).

## Domain rules

### Reserve (single tx, idempotent)

```
tx:
  INSERT INTO credit_reservations (id, user_id, request_id, amount, status)
    VALUES (?, ?, ?, ?, 'reserved') ON CONFLICT(request_id) DO NOTHING;
  SELECT id FROM credit_reservations WHERE request_id = ?;   -- new or existing
  if reservation exists and status != 'reserved' → ErrReservationClosed
  UPDATE credit_accounts SET balance = balance - ? WHERE user_id = ? AND balance >= ?;  -- no-negative guard
  if rowcount == 0 → ROLLBACK → ErrInsufficientCredits
  INSERT INTO credit_transactions (type='reservation', amount=-?, reference_id=reservation_id)
commit
```

### Settle

- `Settle(r, actualCredits)`: capture the reservation to `actualCredits` and
  refund the difference to the balance. `actualCredits` may exceed the reserve
  (see under-reserve policy below).
- Settling is **idempotent**: a second `Settle` on the same reservation does not
  double-refund. The cruft-guard uses the DB committed state, not the passed
  `*Reservation` struct (which callers could tamper with).

### Release

- `Release(r)`: mark the reservation released and return its full amount to the
  balance. Idempotent; calling `Settle` or `Release` on a closed reservation →
  `ErrReservationClosed`.

### Under-reserve policy

When real cost exceeds the reservation, the difference is drawn from the
remaining balance (overage auto-draw) so a slightly low estimate doesn't fail a
successful call. Only if the balance can't cover the overage does `Settle` fail.

### Monthly lazy grant

`EnsureMonthlyGrant(ctx, userID, plan)` grants the configured monthly credits,
idempotent per period (`gap:` idempotency key `monthly:u1:2026-08`). No
scheduler — one line per call; safe concurrent.

### Subscription gate (entitlement)

`SetSubscription(userID, plan, status)` records a subscription whose `status`
(active/paused/cancelled) gates the monthly grant: only `active` plans receive
their monthly credits. `Paused`/`cancelled` plans are skipped. The gate applies
to the monthly grant (entitlement), not to already-granted balances.

## BYOK

### Credential store

- Encryption key: `CREDITS_ENC_KEY` = hex of 32 bytes, read by the **app** and
  passed to `NewCredentialStore`. Without a key the store is disabled
  (`Put`/`Get` return a clear error). Never log the key.
- Cryptography: XChaCha20-Poly1305 (`golang.org/x/crypto/chacha20poly1305`),
  random 24-byte nonce stored beside the ciphertext.
- API keys are **never returned to the browser**: `Get` exists for the relay
  (server-side); public endpoints only expose `{provider, configured: bool}`.

### Relay (`http.Handler`)

- **goai is NOT used in the relay** — it is a pass-through
  (`httputil.ReverseProxy`). The client brings its own SDK (any
  OpenAI-compatible). goai only appears in managed mode, in the app.
- The app mounts the relay behind auth middleware (the relay does not
  authenticate; it trusts the request identity — see below).
- Request form: `POST /api/byok/{provider}/{path...}` (OpenAI-compatible, e.g.
  `/v1/chat/completions`), JSON body identical to OpenAI.
- Flow:
  1. `provider` from path; `apiKey := creds.Get(ctx, userID, provider)` → 404 if
     not configured.
  2. `base := bases[provider]` (map from env `BYOK_PROVIDERS`, e.g.
     `openai:https://api.openai.com/v1`).
  3. `httputil.ReverseProxy` with `Rewrite`: path → `base + /{path...}`, headers
     `Authorization: Bearer <key>` and fallback `X-Api-Key: <key>`, internal
     `X-Byok-*` headers stripped.
  4. Copies status/body — SSE/chunked pass through naturally (no buffering).
  5. **Metering**: wraps the response with `usageRW`, which replays bytes
     untouched and, at the end, extracts the OpenAI-compatible usage (JSON
     non-stream OR the final `data: {…usage…}` chunk before `[DONE]`) and writes
     an `llm_usage` row (`billing_mode=byok`, `credits_charged=0`). JSON capture
     is bounded to 1 MiB; SSE parsing retains only the current event line, so a
     long stream cannot grow memory. Model read from body (best-effort); request
     id from inbound `X-Byok-Request-Id` (`byok:<id>`, idempotent) or a fresh one.

### Identity

The relay reads `userID` from an internal header (`X-Auth-User`) set by app
auth middleware. The app must guarantee that header is only accepted after the
session is validated (strip external headers at the app gateway).

## Security checklist

- Balance: never trust client-side values; grants only via `Grant` with
  `source`/`key`.
- `idempotency_key` UNIQUE is the backstop; an idempotency check must happen
  inside the tx.
- Webhooks (app): verify signature; grant with `key = "stripe:" + payment_intent_id`.
- BYOK: keys encrypted at rest; never in HTML/JS; relay behind auth.
- Logs: never log full prompts or keys.
- `govulncheck` in CI.

## Deliberate simplifications

Trust boundaries and known ceilings, revisited on demand:

- **Single credit pool** — monthly→promo→prepaid ordering would require buckets;
  the ledger already stores `type`. Add when promos/prepaid with expiry appear.
- **Lazy monthly grant** — no scheduler; one row per call. Replace with a job
  when there are many idle users.
- **Relay without rate limit** — per-user, only if real abuse appears.
- **BYOK OpenAI-compatible only** — Anthropic/Gemini have their own formats;
  the provider→base rewrite covers when needed.
- **No auto-fix of balance mismatch in reconcile** — reports; a human decides.
- **No workflow engine** — billing v1 is single-step idempotent ops (webhook →
  Grant, Reserve → Settle, lazy monthly); no multi-step flow justifies it.
- **Streaming settles at end** — usage is recorded when the stream finishes
  (`TotalUsage` arrives on the final chunk); no incremental pre-charge.
- **Usage audit is separate from money settlement** — a provider call cannot
  share a transaction with SQLite. `RecordUsageRetry` makes bounded retries for
  SQLite contention; callers surface the stable request id if audit persistence
  still fails, so it can be repaired idempotently.

## Out of scope (v1) / future

- OpenMeter/ClickHouse/Kafka (when analytical volume justifies).
- Full double-entry, multiple wallets, transfers between users.
- Expiring buckets.
- Per-user rate limiting (app-level).
- Non-OpenAI BYOK provider adapters.
- Analytics beyond simple SQL.