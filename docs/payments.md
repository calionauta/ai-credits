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

Call `payments.ProcessPending(ctx, 100)` periodically and after webhook receipt. Call `payments.Reconcile(ctx)` from a scheduled health job and alert on any returned issues. Stripe retries webhook requests receiving 5xx; invalid signatures receive 400; valid unsupported events receive 204.

Refunds and disputes reverse the original grant. If credits were already consumed, the account may become negative and subsequent reservations fail closed until the debt is covered. This preserves economic truth.

## Webhook events

Supported initially:

- `checkout.session.completed`
- `checkout.session.async_payment_succeeded`
- `charge.refunded`
- `charge.dispute.created`

Provider-specific subscription products remain application-owned. Monthly entitlements should be granted from paid invoice periods using an idempotency key containing the provider subscription ID and period start; never use an approximate 30-day clock.
