// Package stripe verifies Stripe webhooks and maps them to payments events.
package stripe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/calionauta/ai-credits/payments"
	stripego "github.com/stripe/stripe-go/v80"
	"github.com/stripe/stripe-go/v80/checkout/session"
	"github.com/stripe/stripe-go/v80/webhook"
)

type Config struct {
	SecretKey, WebhookSecret, SuccessURL, CancelURL string
}
type Adapter struct {
	payments *payments.Service
	cfg      Config
}
type Checkout struct{ PurchaseID, URL, SessionID string }

func New(p *payments.Service, c Config) (*Adapter, error) {
	if p == nil || c.WebhookSecret == "" {
		return nil, errors.New("stripe: payments and webhook secret required")
	}
	return &Adapter{payments: p, cfg: c}, nil
}

func (a *Adapter) CreatePurchase(ctx context.Context, userID, sku string) (*payments.Purchase, error) {
	return a.payments.CreatePurchase(ctx, "stripe", userID, sku)
}

func (a *Adapter) CreateCheckout(ctx context.Context, userID, sku string) (*Checkout, error) {
	if a.cfg.SecretKey == "" || a.cfg.SuccessURL == "" || a.cfg.CancelURL == "" {
		return nil, errors.New("stripe: checkout configuration required")
	}
	p, err := a.CreatePurchase(ctx, userID, sku)
	if err != nil {
		return nil, err
	}
	mode, quantity, name := string(stripego.CheckoutSessionModePayment), int64(1), "AI credits"
	params := &stripego.CheckoutSessionParams{Mode: &mode, SuccessURL: &a.cfg.SuccessURL, CancelURL: &a.cfg.CancelURL, ClientReferenceID: &p.ID, Metadata: map[string]string{"purchase_id": p.ID}, PaymentIntentData: &stripego.CheckoutSessionPaymentIntentDataParams{Metadata: map[string]string{"purchase_id": p.ID}}, LineItems: []*stripego.CheckoutSessionLineItemParams{{Quantity: &quantity, PriceData: &stripego.CheckoutSessionLineItemPriceDataParams{Currency: &p.Currency, UnitAmount: &p.AmountMinor, ProductData: &stripego.CheckoutSessionLineItemPriceDataProductDataParams{Name: &name}}}}}
	params.SetIdempotencyKey("credits-checkout:" + p.ID)
	client := session.Client{B: stripego.GetBackend(stripego.APIBackend), Key: a.cfg.SecretKey}
	cs, err := client.New(params)
	if err != nil {
		return nil, err
	}
	return &Checkout{PurchaseID: p.ID, URL: cs.URL, SessionID: cs.ID}, nil
}

func (a *Adapter) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, (1<<20)+1))
	if err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	evt, err := webhook.ConstructEvent(body, r.Header.Get("Stripe-Signature"), a.cfg.WebhookSecret)
	if err != nil {
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	h := sha256.Sum256(body)
	hash := hex.EncodeToString(h[:])
	if sub, ok := mapSubscriptionEvent(evt); ok {
		if err = a.payments.ApplySubscription(r.Context(), sub); err != nil {
			http.Error(w, "processing failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	mapped, ok := mapEvent(evt, hash)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err = a.payments.Receive(r.Context(), mapped); err != nil {
		http.Error(w, "processing failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func mapSubscriptionEvent(evt stripego.Event) (payments.SubscriptionEvent, bool) {
	switch evt.Type {
	case "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted":
		var sub stripego.Subscription
		if json.Unmarshal(evt.Data.Raw, &sub) != nil {
			return payments.SubscriptionEvent{}, false
		}
		userID, plan := sub.Metadata["user_id"], sub.Metadata["plan"]
		if userID == "" || plan == "" {
			return payments.SubscriptionEvent{}, false
		}
		status := string(sub.Status)
		if status == "canceled" {
			status = "cancelled"
		}
		return payments.SubscriptionEvent{Provider: "stripe", EventID: evt.ID, SubscriptionID: sub.ID, UserID: userID, Plan: plan, Status: status, PeriodStart: sub.CurrentPeriodStart, PeriodEnd: sub.CurrentPeriodEnd, Created: evt.Created}, true
	default:
		return payments.SubscriptionEvent{}, false
	}
}

func mapEvent(evt stripego.Event, hash string) (payments.Event, bool) {
	switch evt.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		var cs stripego.CheckoutSession
		if json.Unmarshal(evt.Data.Raw, &cs) != nil || cs.PaymentStatus != stripego.CheckoutSessionPaymentStatusPaid {
			return payments.Event{}, false
		}
		pid := ""
		if cs.PaymentIntent != nil {
			pid = cs.PaymentIntent.ID
		}
		purchase := cs.Metadata["purchase_id"]
		if pid == "" || purchase == "" {
			return payments.Event{}, false
		}
		return payments.Event{Provider: "stripe", EventID: evt.ID, PaymentID: pid, PurchaseID: purchase, Status: "paid", PayloadHash: hash, OccurredAt: time.Now()}, true
	case "charge.refunded":
		var ch stripego.Charge
		if json.Unmarshal(evt.Data.Raw, &ch) != nil || ch.PaymentIntent == nil {
			return payments.Event{}, false
		}
		purchase := ch.Metadata["purchase_id"]
		if purchase == "" {
			return payments.Event{}, false
		}
		return payments.Event{Provider: "stripe", EventID: evt.ID, PaymentID: ch.PaymentIntent.ID, PurchaseID: purchase, Status: "refunded", PayloadHash: hash, ReversedMinor: ch.AmountRefunded, OccurredAt: time.Now()}, true
	case "charge.dispute.created":
		var d stripego.Dispute
		if json.Unmarshal(evt.Data.Raw, &d) != nil || d.PaymentIntent == nil {
			return payments.Event{}, false
		}
		purchase := d.Metadata["purchase_id"]
		if purchase == "" && d.Charge != nil {
			purchase = d.Charge.Metadata["purchase_id"]
		}
		if purchase == "" {
			return payments.Event{}, false
		}
		return payments.Event{Provider: "stripe", EventID: evt.ID, PaymentID: d.PaymentIntent.ID, PurchaseID: purchase, Status: "disputed", PayloadHash: hash, ReversedMinor: d.Amount, OccurredAt: time.Now()}, true
	default:
		return payments.Event{}, false
	}
}
