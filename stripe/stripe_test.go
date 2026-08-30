package stripe

import (
	"bytes"
	"context"
	"database/sql"
	"github.com/calionauta/ai-credits/credits"
	"github.com/calionauta/ai-credits/payments"
	"github.com/stripe/stripe-go/v80/webhook"
	_ "modernc.org/sqlite"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
