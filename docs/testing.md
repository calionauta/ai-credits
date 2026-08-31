# Testing

The root commands (unit tests, lint, vuln scan) are in the README. This page
covers the Stripe webhook coverage: the synthetic (offline) tests and the
opt-in E2E suite against real Stripe test-mode.

## Synthetic Stripe webhooks

`webhook.GenerateTestSignedPayload` builds signed payloads that cover the
following events **without any network**:

- `checkout.session.completed`
- `charge.refunded`
- `charge.dispute.created`
- `customer.subscription.*`
- `invoice.*`

Run them with the normal suite (no secrets needed):

```bash
go test -race ./...
```

See `stripe/stripe_test.go` and `payments/payments_test.go`.

## E2E with real Stripe (optional, for contributors)

Integration tests that hit the Stripe test-mode API are **opt-in** and require
test keys. They are not needed for normal library use.

1. Create a Stripe test account and get `STRIPE_SECRET_KEY` (`sk_test_...`) and
   `STRIPE_WEBHOOK_SECRET` (`whsec_...`).
2. Install `stripe-cli` and login: `stripe login`.
3. Run E2E tests:

```bash
export STRIPE_SECRET_KEY=sk_test_...
export STRIPE_WEBHOOK_SECRET=whsec_...
go test -tags=e2e -run TestStripeE2E -count=1 ./stripe -v
# or trigger a real invoice event:
stripe trigger invoice.paid --add invoice:metadata[user_id]=u_test --add invoice:metadata[plan]=pro
```

Without `STRIPE_SECRET_KEY`, `go test ./...` skips the `e2e` tag and still
passes. See `stripe/stripe_e2e_test.go` for the real-API tests.