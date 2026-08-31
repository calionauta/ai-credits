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

	stripego "github.com/stripe/stripe-go/v80"
	"github.com/stripe/stripe-go/v80/checkout/session"
	"github.com/stripe/stripe-go/v80/webhook"

	"github.com/calionauta/ai-credits/payments"
)

// prorationGraceSeconds is the minimum invoice period length considered a full billing cycle.
const prorationGraceSeconds = int64(86400)

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
	params := &stripego.CheckoutSessionParams{
		Mode:              &mode,
		SuccessURL:        &a.cfg.SuccessURL,
		CancelURL:         &a.cfg.CancelURL,
		ClientReferenceID: &p.ID,
		Metadata:          map[string]string{"purchase_id": p.ID},
		PaymentIntentData: &stripego.CheckoutSessionPaymentIntentDataParams{Metadata: map[string]string{"purchase_id": p.ID}},
		LineItems: []*stripego.CheckoutSessionLineItemParams{{
			Quantity: &quantity,
			PriceData: &stripego.CheckoutSessionLineItemPriceDataParams{
				Currency:    &p.Currency,
				UnitAmount:  &p.AmountMinor,
				ProductData: &stripego.CheckoutSessionLineItemPriceDataProductDataParams{Name: &name},
			},
		}},
	}
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
	if handled, herr := handleInvoiceEvent(r.Context(), a, evt); handled {
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

type invoiceShadow struct {
	Parent struct {
		SubscriptionDetails *struct {
			Subscription string            `json:"subscription"`
			Metadata     map[string]string `json:"metadata"`
		} `json:"subscription_details"`
	} `json:"parent"`
	Lines *struct {
		Data []struct {
			Parent *struct {
				SubscriptionItemDetails *struct {
					Subscription string `json:"subscription"`
				} `json:"subscription_item_details"`
			} `json:"parent"`
		} `json:"data"`
	} `json:"lines"`
}

// subID resolves the subscription the invoice belongs to. Stripe sends it as a
// v1 string ID (`invoice.subscription`) or as an expanded v2 object via
// invoice.subscription_details and line `parent.subscription_item_details`;
// stripego.Invoice only surfaces the v1 string, so the v2 shadow paths stay raw.
func invoiceSubID(sub stripego.Invoice, s invoiceShadow) string {
	if sub.Subscription != nil && sub.Subscription.ID != "" {
		return sub.Subscription.ID
	}
	if s.Parent.SubscriptionDetails != nil && s.Parent.SubscriptionDetails.Subscription != "" {
		return s.Parent.SubscriptionDetails.Subscription
	}
	if s.Lines != nil {
		for _, l := range s.Lines.Data {
			if l.Parent != nil && l.Parent.SubscriptionItemDetails != nil && l.Parent.SubscriptionItemDetails.Subscription != "" {
				return l.Parent.SubscriptionItemDetails.Subscription
			}
		}
	}
	return ""
}

func handleInvoiceEvent(ctx context.Context, a *Adapter, evt stripego.Event) (bool, error) {
	switch evt.Type {
	case "invoice.paid", "invoice.payment_failed":
	default:
		return false, nil
	}
	var raw invoiceShadow
	var inv stripego.Invoice
	if err := json.Unmarshal(evt.Data.Raw, &inv); err != nil {
		return true, err
	}
	_ = json.Unmarshal(evt.Data.Raw, &raw)
	subID := invoiceSubID(inv, raw)
	if evt.Type == "invoice.payment_failed" {
		return handleInvoiceFailed(ctx, a, evt, inv, subID)
	}
	return handleInvoicePaid(ctx, a, evt, inv, raw, subID)
}

func resolveUserPlan(ctx context.Context, a *Adapter, inv stripego.Invoice, raw invoiceShadow, subID string) (string, string) {
	userID := ""
	plan := ""
	if inv.Metadata != nil {
		userID = inv.Metadata["user_id"]
		plan = inv.Metadata["plan"]
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
	return userID, plan
}

func handleInvoiceFailed(ctx context.Context, a *Adapter, evt stripego.Event, inv stripego.Invoice, subID string) (bool, error) {
	if subID == "" {
		return true, nil
	}
	userID, plan := resolveUserPlan(ctx, a, inv, invoiceShadow{}, subID)
	if userID != "" && plan != "" {
		_ = a.payments.ApplySubscription(ctx, payments.SubscriptionEvent{
			Provider: "stripe", EventID: evt.ID, SubscriptionID: subID, UserID: userID, Plan: plan,
			Status: "past_due", PeriodStart: inv.PeriodStart, PeriodEnd: inv.PeriodEnd, Created: evt.Created,
		})
	}
	return true, nil
}

func handleInvoicePaid(ctx context.Context, a *Adapter, _ stripego.Event, inv stripego.Invoice, raw invoiceShadow, subID string) (bool, error) {
	if string(inv.Status) != "" && string(inv.Status) != "paid" {
		return true, nil
	}
	if string(inv.BillingReason) == "manual" {
		return true, nil
	}
	if string(inv.BillingReason) == "subscription_update" {
		isProration := false
		if inv.Lines != nil {
			for _, l := range inv.Lines.Data {
				if l.Period != nil && l.Period.End-l.Period.Start < prorationGraceSeconds {
					isProration = true
					break
				}
			}
		}
		if isProration {
			slog.Info("stripe: proration invoice skipped full grant", "invoice", inv.ID, "subscription", subID)
			return true, nil
		}
	}
	if subID == "" {
		slog.Warn("stripe: invoice.paid without subscription", "invoice", inv.ID)
		return true, nil
	}
	periodStart := inv.PeriodStart
	if periodStart == 0 && inv.Lines != nil && len(inv.Lines.Data) > 0 && inv.Lines.Data[0].Period != nil {
		periodStart = inv.Lines.Data[0].Period.Start
	}
	if periodStart == 0 {
		slog.Warn("stripe: invoice.paid missing period_start", "invoice", inv.ID, "subscription", subID)
		return true, nil
	}
	userID, plan := resolveUserPlan(ctx, a, inv, raw, subID)
	if userID == "" || plan == "" {
		slog.Warn("stripe: invoice.paid missing user/plan", "invoice", inv.ID, "subscription", subID)
		return true, nil
	}
	var credits int64
	if a.cfg.SubscriptionCredits != nil {
		if v, ok := a.cfg.SubscriptionCredits[plan]; ok {
			credits = v
		}
	}
	if credits == 0 {
		slog.Info("stripe: invoice.paid no credits mapping for plan", "plan", plan, "invoice", inv.ID)
		return true, nil
	}
	if err := a.payments.GrantSubscriptionPeriod(ctx, "stripe", subID, inv.ID, userID, credits, periodStart); err != nil {
		return true, err
	}
	return true, nil
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
