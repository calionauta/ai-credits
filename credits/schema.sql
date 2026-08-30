-- ai-credits schema — idempotent (CREATE TABLE IF NOT EXISTS), owned by the lib.
-- Timestamps are INTEGER unix (UTC). Source of truth = credit_transactions.

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS credit_accounts (
    user_id    TEXT PRIMARY KEY,
    balance    INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS credit_transactions (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    amount          INTEGER NOT NULL,
    type            TEXT NOT NULL,
    source          TEXT NOT NULL,
    reference_id    TEXT,
    idempotency_key TEXT UNIQUE,
    metadata        TEXT,
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_credit_tx_user ON credit_transactions(user_id, created_at);

CREATE TABLE IF NOT EXISTS credit_reservations (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    request_id      TEXT UNIQUE NOT NULL,
    amount          INTEGER NOT NULL,
    captured_amount INTEGER NOT NULL DEFAULT 0,
    released_amount INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_credit_reservations_stale ON credit_reservations(status, created_at);

CREATE TABLE IF NOT EXISTS llm_usage (
    id                        TEXT PRIMARY KEY,
    request_id                TEXT UNIQUE NOT NULL,
    user_id                   TEXT NOT NULL,
    provider                  TEXT NOT NULL,
    model                     TEXT NOT NULL,
    billing_mode              TEXT NOT NULL,
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
    provider      TEXT NOT NULL,
    encrypted_key BLOB NOT NULL,
    version       INTEGER NOT NULL DEFAULT 1,
    previous_key  BLOB,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (user_id, provider)
);

CREATE TABLE IF NOT EXISTS subscriptions (
    user_id    TEXT PRIMARY KEY,
    plan       TEXT NOT NULL,
    status     TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- Durable settlement outbox for Ensaiter: ensures Reserve->Settle survives crash after provider returns.
CREATE TABLE IF NOT EXISTS settlement_outbox (
    request_id   TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    reservation_id TEXT NOT NULL,
    provider     TEXT NOT NULL,
    model        TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_settlement_outbox_pending ON settlement_outbox(status, created_at);
