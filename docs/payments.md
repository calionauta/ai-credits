# Optional payments and Stripe

The root `credits` package remains provider-free. Optional nested modules add reusable payment fulfillment:

- `github.com/calionauta/ai-credits/payments`: server-owned catalog, immutable purchase snapshots, durable webhook receipts, idempotent grant/reversal, retry and reconciliation.
- `github.com/calionauta/ai-credits/stripe`: Stripe Checkout and signed webhook translation.

```go
payments, _ := paymentcore.New(db, creditsService, map[string]paymentcore.CatalogItem{
    "topup-small": {Credits: 500, Currency: "usd", AmountMinor: 500},
})
stripeAdapter, _ := stripecredits.New(payments, stripecredits.Config{
    SecretKey: os.Getenv("STRIPE_SECRET_KEY"),
    WebhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
    SuccessURL: "https://app.example/credits?status=success",
    CancelURL: "https://app.example/credits?status=cancelled",
})
```

The browser submits only an allowlisted SKU. Stripe metadata carries an opaque local purchase ID, never user-controlled credits or prices. Verified events are durably received before processing. Delivery idempotency uses provider/event ID; economic idempotency uses provider/payment ID and the core ledger key.

## Operations

Call `payments.ProcessPending(ctx, 100)` periodically and after webhook receipt, or run `payments.NewWorker(...).Run(ctx)` for loop with backoff/dead-letter. Call `payments.Reconcile(ctx)` or `ReconcileWithStripe(ctx, secret)` from a scheduled health job and alert on any returned issues. For durable LLM settlement, call `credits.EnqueueSettlement` after `Reserve` and `SettleViaOutbox` after provider returns; run `ProcessSettlementOutbox` every minute. BYOK relay uses explicit `UpstreamTimeout` (default 30s). Stripe retries webhook requests receiving 5xx; invalid signatures receive 400; valid unsupported events receive 204.

Refunds and disputes reverse the original grant. If credits were already consumed, the account may become negative and subsequent reservations fail closed until the debt is covered. This preserves economic truth.

## Webhook events

Supported events:

- `checkout.session.completed`
- `checkout.session.async_payment_succeeded`
- `charge.refunded` (cumulative proportional credit reversal)
- `charge.dispute.created`
- `customer.subscription.created|updated|deleted` (with trial_start/trial_end, cancel_at, cancel_at_period_end)
- `invoice.paid` -> auto `GrantSubscriptionPeriod` when `SubscriptionCredits[plan]` configured (handles subscription_cycle/create, skips proration subscription_update)
- `invoice.payment_failed` -> marks subscription `past_due`

Subscription state stores exact provider period boundaries, trial and cancellation metadata (`trial_start`, `trial_end`, `cancel_at`, `cancel_at_period_end`, `canceled_at`) and rejects stale out-of-order updates. `GrantSubscriptionPeriod` grants entitlements exactly once per provider, subscription, period start, and invoice. Configure `stripe.Config{SubscriptionCredits: map[plan]credits}` for auto invoice handling; otherwise call helper manually for verified paid invoices. Upgrades/downgrades mid-cycle send proration invoices (short period) which are skipped for full grant — plan change is tracked via `subscription.updated`.

## Security and recovery

Payment events are leased to workers. An expired `processing` lease is returned
to the queue after a crash; ledger idempotency keys make replay safe. Duplicate
event IDs with different payload hashes are rejected. Reconciliation reports
pending receipts, stale purchases, and fulfilled purchases without an applied
receipt.

BYOK upstreams require HTTPS, except explicit loopback HTTP for local testing.
Request bodies are capped at 1 MiB and rejected with 413 instead of forwarding
an empty request.
