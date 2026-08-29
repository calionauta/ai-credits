-- ai-credits schema — idempotent (CREATE TABLE IF NOT EXISTS), owned by the lib.
-- Timestamps are INTEGER unix (UTC). Source of truth = credit_transactions.

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS credit_accounts (
    user_id    TEXT PRIMARY KEY,
    balance    INTEGER NOT NULL DEFAULT 0,   -- materialized credit balance
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS credit_transactions (
    id              TEXT PRIMARY KEY,          -- hex crypto/rand 16 bytes
    user_id         TEXT NOT NULL,
    amount          INTEGER NOT NULL,          -- +/- credits
    type            TEXT NOT NULL,             -- grant|monthly|topup|refund|reservation|reservation_release|reservation_overage|adjustment
    source          TEXT NOT NULL,             -- signup|admin|stripe|monthly|llm_request|reconcile
    reference_id    TEXT,                      -- reservation_id | payment_intent_id | request_id
    idempotency_key TEXT UNIQUE,               -- e.g. "stripe:pi_123", "monthly:u1:2026-08", "req:abc"
    metadata        TEXT,                      -- optional JSON
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_credit_tx_user ON credit_transactions(user_id, created_at);

CREATE TABLE IF NOT EXISTS credit_reservations (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    request_id      TEXT UNIQUE NOT NULL,      -- reservation idempotency
    amount          INTEGER NOT NULL,          -- reserved credits (= max estimate)
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
    provider      TEXT NOT NULL,               -- "openai"|"openrouter"|... (key of provider map)
    encrypted_key BLOB NOT NULL,               -- nonce || XChaCha20-Poly1305(apiKey)
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (user_id, provider)
);

CREATE TABLE IF NOT EXISTS subscriptions (
    user_id    TEXT PRIMARY KEY,
    plan       TEXT NOT NULL,                  -- plan key used in PlanMonthlyCredits
    status     TEXT NOT NULL,                  -- active|cancelled|paused
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);