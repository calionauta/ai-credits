// Package stripe verifies Stripe webhooks and maps them to payments events.
package stripe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/calionauta/ai-credits/payments"
	stripego "github.com/stripe/stripe-go/v80"
	"github.com/stripe/stripe-go/v80/checkout/session"
	"github.com/stripe/stripe-go/v80/webhook"
)

type Config struct {
	SecretKey, WebhookSecret, SuccessURL, CancelURL string
	SubscriptionCredits                             map[string]int64
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
	if handled, herr := a.handleInvoiceEvent(r.Context(), evt); handled {
		if herr != nil {
			slog.Warn("stripe: invoice handling", "type", evt.Type, "err", herr)
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
		switch status {
		case "incomplete", "incomplete_expired":
			status = "past_due"
		}
		// Handle trial and cancel_at_period_end for complete lifecycle
		cancelAtPeriodEnd := false
		if sub.CancelAtPeriodEnd {
			cancelAtPeriodEnd = true
		}
		// If cancel_at_period_end is true and status is active, future cancellation is pending but current period remains active
		// Trial termination: when trial_end passes, status moves from trialing to active
		return payments.SubscriptionEvent{
			Provider: "stripe", EventID: evt.ID, SubscriptionID: sub.ID, UserID: userID, Plan: plan, Status: status,
			PeriodStart: sub.CurrentPeriodStart, PeriodEnd: sub.CurrentPeriodEnd, Created: evt.Created,
			TrialStart: sub.TrialStart, TrialEnd: sub.TrialEnd, CancelAt: sub.CancelAt, CancelAtPeriodEnd: cancelAtPeriodEnd,
		}, true
	default:
		return payments.SubscriptionEvent{}, false
	}
}

func (a *Adapter) handleInvoiceEvent(ctx context.Context, evt stripego.Event) (bool, error) {
	switch evt.Type {
	case "invoice.paid", "invoice.payment_failed":
	default:
		return false, nil
	}
	var raw struct {
		ID            string            `json:"id"`
		Status        string            `json:"status"`
		BillingReason string            `json:"billing_reason"`
		Subscription  json.RawMessage   `json:"subscription"`
		Customer      json.RawMessage   `json:"customer"`
		AmountPaid    int64             `json:"amount_paid"`
		Currency      string            `json:"currency"`
		PeriodStart   int64             `json:"period_start"`
		PeriodEnd     int64             `json:"period_end"`
		Metadata      map[string]string `json:"metadata"`
		Parent        struct {
			SubscriptionDetails *struct {
				Subscription string            `json:"subscription"`
				Metadata     map[string]string `json:"metadata"`
			} `json:"subscription_details"`
		} `json:"parent"`
		Lines *struct {
			Data []struct {
				Period struct {
					Start int64 `json:"start"`
					End   int64 `json:"end"`
				} `json:"period"`
				Parent *struct {
					SubscriptionItemDetails *struct {
						Subscription string `json:"subscription"`
					} `json:"subscription_item_details"`
				} `json:"parent"`
			} `json:"data"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(evt.Data.Raw, &raw); err != nil {
		return true, err
	}
	subID := extractStringID(raw.Subscription)
	if subID == "" && raw.Parent.SubscriptionDetails != nil {
		subID = raw.Parent.SubscriptionDetails.Subscription
	}
	if subID == "" && raw.Lines != nil {
		for _, l := range raw.Lines.Data {
			if l.Parent != nil && l.Parent.SubscriptionItemDetails != nil && l.Parent.SubscriptionItemDetails.Subscription != "" {
				subID = l.Parent.SubscriptionItemDetails.Subscription
				break
			}
		}
	}
	if evt.Type == "invoice.payment_failed" {
		if subID != "" {
			userID := ""
			plan := ""
			if raw.Metadata != nil {
				userID = raw.Metadata["user_id"]
				plan = raw.Metadata["plan"]
			}
			if (userID == "" || plan == "") && a.payments != nil {
				if sub, err := a.payments.Subscription(ctx, "stripe", subID); err == nil && sub != nil {
					if userID == "" {
						userID = sub.UserID
					}
					if plan == "" {
						plan = sub.Plan
					}
				}
			}
			if userID != "" && plan != "" {
				_ = a.payments.ApplySubscription(ctx, payments.SubscriptionEvent{
					Provider: "stripe", EventID: evt.ID, SubscriptionID: subID, UserID: userID, Plan: plan,
					Status: "past_due", PeriodStart: raw.PeriodStart, PeriodEnd: raw.PeriodEnd, Created: evt.Created,
				})
			}
		}
		return true, nil
	}
	if raw.Status != "" && raw.Status != "paid" {
		return true, nil
	}
	if raw.BillingReason == "manual" {
		return true, nil
	}
	if raw.BillingReason == "subscription_update" {
		isProration := false
		if raw.Lines != nil {
			for _, l := range raw.Lines.Data {
				if l.Period.End-l.Period.Start < 86400 {
					isProration = true
					break
				}
			}
		}
		if isProration {
			slog.Info("stripe: proration invoice skipped full grant", "invoice", raw.ID, "subscription", subID)
			return true, nil
		}
	}
	if subID == "" {
		slog.Warn("stripe: invoice.paid without subscription", "invoice", raw.ID)
		return true, nil
	}
	periodStart := raw.PeriodStart
	if periodStart == 0 && raw.Lines != nil && len(raw.Lines.Data) > 0 {
		periodStart = raw.Lines.Data[0].Period.Start
	}
	if periodStart == 0 {
		slog.Warn("stripe: invoice.paid missing period_start", "invoice", raw.ID, "subscription", subID)
		return true, nil
	}
	userID := ""
	plan := ""
	if raw.Metadata != nil {
		userID = raw.Metadata["user_id"]
		plan = raw.Metadata["plan"]
	}
	if (userID == "" || plan == "") && raw.Parent.SubscriptionDetails != nil && raw.Parent.SubscriptionDetails.Metadata != nil {
		if userID == "" {
			userID = raw.Parent.SubscriptionDetails.Metadata["user_id"]
		}
		if plan == "" {
			plan = raw.Parent.SubscriptionDetails.Metadata["plan"]
		}
	}
	if (userID == "" || plan == "") && a.payments != nil {
		if sub, err := a.payments.Subscription(ctx, "stripe", subID); err == nil && sub != nil {
			if userID == "" {
				userID = sub.UserID
			}
			if plan == "" {
				plan = sub.Plan
			}
		}
	}
	if userID == "" || plan == "" {
		slog.Warn("stripe: invoice.paid missing user/plan", "invoice", raw.ID, "subscription", subID)
		return true, nil
	}
	var credits int64
	if a.cfg.SubscriptionCredits != nil {
		if v, ok := a.cfg.SubscriptionCredits[plan]; ok {
			credits = v
		}
	}
	if credits == 0 {
		slog.Info("stripe: invoice.paid no credits mapping for plan", "plan", plan, "invoice", raw.ID)
		return true, nil
	}
	if err := a.payments.GrantSubscriptionPeriod(ctx, "stripe", subID, raw.ID, userID, credits, periodStart); err != nil {
		return true, err
	}
	return true, nil
}

func extractStringID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.ID
	}
	return ""
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
