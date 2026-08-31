package stripe

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v80/webhook"
	_ "modernc.org/sqlite"

	"github.com/calionauta/ai-credits/credits"
	"github.com/calionauta/ai-credits/payments"
)

func TestSubscriptionWebhookMapsPeriodAndStatus(t *testing.T) {
	db, _ := sql.Open("sqlite", "file:stripe-sub?mode=memory&cache=shared")
	defer db.Close()
	ledger, _ := credits.New(db, credits.Config{})
	p, _ := payments.New(db, ledger, map[string]payments.CatalogItem{})
	a, _ := New(p, Config{WebhookSecret: "whsec_test"})
	payload := []byte(`{"id":"evt_sub","created":20,"api_version":"2024-09-30.acacia","type":"customer.subscription.updated","data":{"object":{"id":"sub_1","status":"active","current_period_start":100,"current_period_end":200,"metadata":{"user_id":"u","plan":"pro"}}}}`)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: "whsec_test", Timestamp: time.Now()})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(signed.Payload))
	req.Header.Set("Stripe-Signature", signed.Header)
	a.HandleWebhook(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status=%d %s", rr.Code, rr.Body.String())
	}
	sub, err := p.Subscription(context.Background(), "stripe", "sub_1")
	if err != nil || sub.Status != "active" || sub.PeriodStart != 100 {
		t.Fatalf("sub=%+v err=%v", sub, err)
	}
}

func TestWebhookFulfillmentIsIdempotent(t *testing.T) {
	db, _ := sql.Open("sqlite", "file:stripe?mode=memory&cache=shared")
	defer db.Close()
	l, err := credits.New(db, credits.Config{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := payments.New(db, l, map[string]payments.CatalogItem{"topup": {Credits: 50, Currency: "usd", AmountMinor: 500}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(p, Config{WebhookSecret: "whsec_test"})
	if err != nil {
		t.Fatal(err)
	}
	purchase, err := a.CreatePurchase(context.Background(), "u", "topup")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"id":"evt_1","api_version":"2024-09-30.acacia","type":"checkout.session.completed","data":{"object":{"id":"cs_1","payment_intent":"pi_1","payment_status":"paid","metadata":{"purchase_id":"` + purchase.ID + `"}}}}`)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: "whsec_test", Timestamp: time.Now()})
	for range 2 {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(signed.Payload))
		req.Header.Set("Stripe-Signature", signed.Header)
		a.HandleWebhook(rr, req)
		if rr.Code != 200 {
			t.Fatalf("status=%d: %s", rr.Code, rr.Body.String())
		}
	}
	b, _ := l.Balance(context.Background(), "u")
	if b != 50 {
		t.Fatalf("balance %d", b)
	}
}

func TestInvoicePaidAutoGrant(t *testing.T) {
	db, _ := sql.Open("sqlite", "file:stripe-invoice-paid?mode=memory&cache=shared")
	defer db.Close()
	ledger, _ := credits.New(db, credits.Config{})
	p, _ := payments.New(db, ledger, map[string]payments.CatalogItem{})
	a, _ := New(p, Config{WebhookSecret: "whsec_test", SubscriptionCredits: map[string]int64{"pro": 100}})
	// invoice.paid for pro plan, subscription_cycle, period 100-200
	payload := []byte(`{"id":"evt_inv_paid","api_version":"2024-09-30.acacia","created":30,"type":"invoice.paid","data":{"object":{"id":"in_1","status":"paid","billing_reason":"subscription_cycle","subscription":"sub_1","period_start":100,"period_end":200,"lines":{"data":[{"period":{"start":100,"end":200}}]},"metadata":{"user_id":"u","plan":"pro"}}}}`)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: "whsec_test", Timestamp: time.Now()})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(signed.Payload))
	req.Header.Set("Stripe-Signature", signed.Header)
	a.HandleWebhook(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status=%d %s", rr.Code, rr.Body.String())
	}
	bal, _ := ledger.Balance(context.Background(), "u")
	if bal != 100 {
		t.Fatalf("balance=%d want 100 after invoice.paid auto-grant", bal)
	}
	// idempotent retry
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(signed.Payload))
	req2.Header.Set("Stripe-Signature", signed.Header)
	a.HandleWebhook(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("retry status=%d", rr2.Code)
	}
	bal2, _ := ledger.Balance(context.Background(), "u")
	if bal2 != 100 {
		t.Fatalf("idempotent retry balance=%d want 100", bal2)
	}
}

func TestInvoiceProrationSkipped(t *testing.T) {
	db, _ := sql.Open("sqlite", "file:stripe-proration?mode=memory&cache=shared")
	defer db.Close()
	ledger, _ := credits.New(db, credits.Config{})
	p, _ := payments.New(db, ledger, map[string]payments.CatalogItem{})
	a, _ := New(p, Config{WebhookSecret: "whsec_test", SubscriptionCredits: map[string]int64{"pro": 100}})
	// subscription_update with short period (<86400) is proration, should be skipped
	payload := []byte(`{"id":"evt_proration","api_version":"2024-09-30.acacia","created":31,"type":"invoice.paid","data":{"object":{"id":"in_pror","status":"paid","billing_reason":"subscription_update","subscription":"sub_1","period_start":100,"lines":{"data":[{"period":{"start":100,"end":200}}]},"metadata":{"user_id":"u","plan":"pro"}}}}`)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: "whsec_test", Timestamp: time.Now()})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(signed.Payload))
	req.Header.Set("Stripe-Signature", signed.Header)
	a.HandleWebhook(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status=%d", rr.Code)
	}
	bal, _ := ledger.Balance(context.Background(), "u")
	if bal != 0 {
		t.Fatalf("proration should not grant, balance=%d", bal)
	}
}

func TestInvoicePaymentFailedMarksPastDue(t *testing.T) {
	db, _ := sql.Open("sqlite", "file:stripe-failed?mode=memory&cache=shared")
	defer db.Close()
	ledger, _ := credits.New(db, credits.Config{})
	p, _ := payments.New(db, ledger, map[string]payments.CatalogItem{})
	// pre-create subscription active
	_ = p.ApplySubscription(context.Background(), payments.SubscriptionEvent{Provider: "stripe", EventID: "evt_init", SubscriptionID: "sub_1", UserID: "u", Plan: "pro", Status: "active", PeriodStart: 100, PeriodEnd: 200, Created: 10})
	a, _ := New(p, Config{WebhookSecret: "whsec_test"})
	payload := []byte(`{"id":"evt_failed","api_version":"2024-09-30.acacia","created":32,"type":"invoice.payment_failed","data":{"object":{"id":"in_fail","status":"open","billing_reason":"subscription_cycle","subscription":"sub_1","period_start":100,"metadata":{"user_id":"u","plan":"pro"}}}}`)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: "whsec_test", Timestamp: time.Now()})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(signed.Payload))
	req.Header.Set("Stripe-Signature", signed.Header)
	a.HandleWebhook(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status=%d %s", rr.Code, rr.Body.String())
	}
	sub, _ := p.Subscription(context.Background(), "stripe", "sub_1")
	if sub.Status != "past_due" {
		t.Fatalf("expected past_due after payment_failed, got %s", sub.Status)
	}
}

func TestSubscriptionTrialAndCancelFields(t *testing.T) {
	db, _ := sql.Open("sqlite", "file:stripe-trial?mode=memory&cache=shared")
	defer db.Close()
	ledger, _ := credits.New(db, credits.Config{})
	p, _ := payments.New(db, ledger, map[string]payments.CatalogItem{})
	a, _ := New(p, Config{WebhookSecret: "whsec_test"})
	payload := []byte(`{"id":"evt_trial","api_version":"2024-09-30.acacia","created":40,"type":"customer.subscription.updated","data":{"object":{"id":"sub_trial","status":"trialing","current_period_start":100,"current_period_end":200,"trial_start":90,"trial_end":110,"cancel_at_period_end":true,"cancel_at":500,"metadata":{"user_id":"u","plan":"pro"}}}}`)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: "whsec_test", Timestamp: time.Now()})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(signed.Payload))
	req.Header.Set("Stripe-Signature", signed.Header)
	a.HandleWebhook(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status=%d", rr.Code)
	}
	sub, _ := p.Subscription(context.Background(), "stripe", "sub_trial")
	if sub.TrialEnd != 110 || !sub.CancelAtPeriodEnd {
		t.Fatalf("trial/cancel not persisted: %+v", sub)
	}
}
