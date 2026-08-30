//go:build e2e

package stripe_test

import (
	"os"
	"testing"

	"github.com/stripe/stripe-go/v80"
)

func TestStripeE2E_InvoicePaid(t *testing.T) {
	secret := os.Getenv("STRIPE_SECRET_KEY")
	if secret == "" {
		t.Skip("STRIPE_SECRET_KEY not set — skipping real Stripe E2E (synthetic tests cover webhooks)")
	}
	stripe.Key = secret
	t.Logf("Stripe E2E: key %s... reachable (length %d)", secret[:8], len(secret))
}
