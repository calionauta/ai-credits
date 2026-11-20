package stripe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	_ "turso.tech/database/tursogo"

	"github.com/calionauta/ai-credits/credits"
	"github.com/calionauta/ai-credits/payments"
)

// newCheckoutAdapter builds an Adapter on a fresh file-backed DB with one
// purchasable SKU. CreateCheckout writes a purchase row before it talks to
// Stripe, so the catalog has to be real, not a stub.
func newCheckoutAdapter(t *testing.T, backendURL string) (*Adapter, *payments.Service) {
	t.Helper()
	db := testFileDB(t, "stripe-checkout")
	ledger, err := credits.New(db, credits.Config{})
	if err != nil {
		t.Fatalf("credits.New: %v", err)
	}
	p, err := payments.New(db, ledger, map[string]payments.CatalogItem{
		"topup": {Credits: 50, Currency: "USD", AmountMinor: 500},
	})
	if err != nil {
		t.Fatalf("payments.New: %v", err)
	}
	a, err := New(p, Config{
		SecretKey: "sk_test_offline", WebhookSecret: "whsec_test",
		SuccessURL: "https://app.test/credits?status=success",
		CancelURL:  "https://app.test/credits?status=cancelled",
		BackendURL: backendURL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, p
}

// TestCreateCheckoutAgainstInjectedBackend covers the one outbound Stripe call
// the adapter makes. Without Config.BackendURL this path could only be reached
// with a live test-mode account, which is exactly why it had no offline test:
// the webhook suite proves the inbound half, and the outbound half (create a
// purchase row, POST a Checkout Session, hand back its URL) went unexercised.
//
// It asserts the wire contract that matters: the request goes to the injected
// base URL, carries the idempotency key derived from the purchase, and the
// line item is priced from the local catalog — not from what the server echoes.
func TestCreateCheckoutAgainstInjectedBackend(t *testing.T) {
	var hits int
	var form map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/v1/checkout/sessions" {
			t.Errorf("path=%q want /v1/checkout/sessions", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		form = map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		form["Idempotency-Key"] = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_123","object":"checkout.session","url":"https://checkout.stripe.test/c/cs_test_123"}`))
	}))
	defer srv.Close()

	a, p := newCheckoutAdapter(t, srv.URL)
	out, err := a.CreateCheckout(context.Background(), "u_checkout", "topup")
	if err != nil {
		t.Fatalf("CreateCheckout: %v", err)
	}
	if hits != 1 {
		t.Fatalf("backend hits=%d want 1", hits)
	}

	// The returned value is what a caller renders: the purchase row id and the
	// redirect URL the browser must reach.
	if out.URL != "https://checkout.stripe.test/c/cs_test_123" || out.SessionID != "cs_test_123" {
		t.Fatalf("checkout=%+v", out)
	}
	purchase, err := p.Purchase(context.Background(), out.PurchaseID)
	if err != nil {
		t.Fatalf("Purchase(%q): %v", out.PurchaseID, err)
	}
	if purchase.UserID != "u_checkout" || purchase.SKU != "topup" || purchase.Status != "pending" {
		t.Fatalf("purchase=%+v", purchase)
	}

	want := map[string]string{
		"Idempotency-Key":       "credits-checkout:" + out.PurchaseID,
		"mode":                  "payment",
		"success_url":           "https://app.test/credits?status=success",
		"cancel_url":            "https://app.test/credits?status=cancelled",
		"client_reference_id":   out.PurchaseID,
		"metadata[purchase_id]": out.PurchaseID,
		"payment_intent_data[metadata][purchase_id]":    out.PurchaseID,
		"line_items[0][quantity]":                       "1",
		"line_items[0][price_data][currency]":           "usd",
		"line_items[0][price_data][unit_amount]":        strconv.FormatInt(purchase.AmountMinor, 10),
		"line_items[0][price_data][product_data][name]": "AI credits",
	}
	for k, v := range want {
		if got := form[k]; got != v {
			t.Errorf("%s=%q want %q", k, got, v)
		}
	}
}

// TestCreateCheckoutRequiresConfiguration pins the guard rather than the call:
// a server configured with only a webhook secret must refuse checkout before
// it creates a purchase row, so a misconfigured deployment 500s on one route
// instead of leaving orphaned pending purchases behind.
func TestCreateCheckoutRequiresConfiguration(t *testing.T) {
	db := testFileDB(t, "stripe-checkout-unconfigured")
	ledger, err := credits.New(db, credits.Config{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := payments.New(db, ledger, map[string]payments.CatalogItem{
		"topup": {Credits: 50, Currency: "USD", AmountMinor: 500},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(p, Config{SecretKey: "sk_test_offline", WebhookSecret: "whsec_test"}) // no success/cancel URL
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateCheckout(context.Background(), "u", "topup"); err == nil {
		t.Fatal("CreateCheckout with no success/cancel URL: want error, got none")
	}
}
