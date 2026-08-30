CREATE TABLE IF NOT EXISTS payments_schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS payment_purchases (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  user_id TEXT NOT NULL,
  sku TEXT NOT NULL,
  credits INTEGER NOT NULL CHECK(credits > 0),
  currency TEXT NOT NULL,
  amount_minor INTEGER NOT NULL CHECK(amount_minor > 0),
  payment_id TEXT,
  status TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  fulfilled_at INTEGER,
  reversed_at INTEGER,
  reversed_credits INTEGER NOT NULL DEFAULT 0,
  UNIQUE(provider, payment_id)
);
CREATE TABLE IF NOT EXISTS payment_events (
  provider TEXT NOT NULL,
  event_id TEXT NOT NULL,
  purchase_id TEXT NOT NULL,
  payment_id TEXT NOT NULL,
  status TEXT NOT NULL,
  payload_hash TEXT,
  process_status TEXT NOT NULL DEFAULT 'received',
  attempt_count INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  received_at INTEGER NOT NULL,
  processed_at INTEGER,
  lease_until INTEGER,
  reversed_minor INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(provider,event_id)
);
CREATE INDEX IF NOT EXISTS idx_payment_purchases_user ON payment_purchases(user_id,created_at);
CREATE INDEX IF NOT EXISTS idx_payment_events_pending ON payment_events(process_status,received_at);
CREATE TABLE IF NOT EXISTS payment_subscriptions (
  provider TEXT NOT NULL, subscription_id TEXT NOT NULL, user_id TEXT NOT NULL,
  plan TEXT NOT NULL, status TEXT NOT NULL, period_start INTEGER NOT NULL,
  period_end INTEGER NOT NULL, last_event_created INTEGER NOT NULL,
  updated_at INTEGER NOT NULL, PRIMARY KEY(provider,subscription_id)
);
