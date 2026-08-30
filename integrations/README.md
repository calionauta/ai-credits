# Integrations

This directory previously contained duplicated `payments` and `stripe` packages.

**Use the canonical packages at the repository root:**

- `github.com/calionauta/ai-credits/payments` — idempotent purchases, durable events, subscriptions, worker, reconciliation
- `github.com/calionauta/ai-credits/stripe` — Checkout + webhook translation (including `invoice.paid` auto-grant)

`integrations/` is kept only for historical links; do not import from it. See `docs/payments.md` and `README.md` for usage.
