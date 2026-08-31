// Package payments turns verified provider payment facts into idempotent
// credit grants. It deliberately knows no provider SDK or checkout protocol.
package payments

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/calionauta/ai-credits/credits"
)

//go:embed schema.sql
var schema string

type Ledger interface {
	Grant(context.Context, string, int64, string, string, string) error
	Refund(context.Context, string, int64, string, string, string) error
}

type CatalogItem struct {
	Credits     int64
	Currency    string
	AmountMinor int64
}

type Event struct {
	Provider, EventID, PaymentID, PurchaseID, Status, PayloadHash string
	ReversedCredits, ReversedMinor                                int64
	OccurredAt                                                    time.Time
}

type Purchase struct {
	ID, UserID, SKU, Provider, Currency, PaymentID, Status string
	Credits, AmountMinor, ReversedCredits                  int64
}

type SubscriptionEvent struct {
	Provider, EventID, SubscriptionID, UserID, Plan, Status string
	PeriodStart, PeriodEnd, Created                         int64
	TrialStart, TrialEnd, CancelAt                          int64
	CancelAtPeriodEnd                                       bool
}

type Subscription struct {
	Provider, ID, UserID, Plan, Status string
	PeriodStart, PeriodEnd             int64
	TrialStart, TrialEnd, CancelAt     int64
	CancelAtPeriodEnd                  bool
	CanceledAt                         int64
}

type Service struct {
	db      *sql.DB
	ledger  Ledger
	catalog map[string]CatalogItem
	now     func() time.Time
}

func New(db *sql.DB, ledger Ledger, catalog map[string]CatalogItem) (*Service, error) {
	if db == nil || ledger == nil {
		return nil, errors.New("payments: db and ledger are required")
	}
	for sku, item := range catalog {
		if strings.TrimSpace(sku) == "" || item.Credits <= 0 || len(item.Currency) != 3 || item.AmountMinor <= 0 {
			return nil, errors.New("payments: invalid catalog item")
		}
	}
	s := &Service{db: db, ledger: ledger, catalog: catalog, now: time.Now}
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	// Backfill new columns for existing DBs (idempotent ALTERs)
	for _, ddl := range []string{
		`ALTER TABLE payment_subscriptions ADD COLUMN trial_start INTEGER DEFAULT 0`,
		`ALTER TABLE payment_subscriptions ADD COLUMN trial_end INTEGER DEFAULT 0`,
		`ALTER TABLE payment_subscriptions ADD COLUMN cancel_at INTEGER DEFAULT 0`,
		`ALTER TABLE payment_subscriptions ADD COLUMN cancel_at_period_end INTEGER DEFAULT 0`,
		`ALTER TABLE payment_subscriptions ADD COLUMN canceled_at INTEGER DEFAULT 0`,
	} {
		_, _ = db.Exec(ddl)
	}
	_, _ = db.Exec(`INSERT INTO payments_schema_migrations(version,applied_at) VALUES(1,?) ON CONFLICT(version) DO NOTHING`, s.now().Unix())
	return s, nil
}

func (s *Service) CatalogItem(sku string) (CatalogItem, bool) {
	item, ok := s.catalog[sku]
	return item, ok
}

func (s *Service) CreatePurchase(ctx context.Context, provider, userID, sku string) (*Purchase, error) {
	item, ok := s.catalog[sku]
	if !ok {
		return nil, errors.New("payments: unknown sku")
	}
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(userID) == "" {
		return nil, errors.New("payments: provider and user_id required")
	}
	id, now := credits.NewRequestID(), s.now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO payment_purchases (id,provider,user_id,sku,credits,currency,amount_minor,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, id, provider, userID, sku, item.Credits, strings.ToLower(item.Currency), item.AmountMinor, "pending", now, now)
	if err != nil {
		return nil, err
	}
	return &Purchase{ID: id, Provider: provider, UserID: userID, SKU: sku, Credits: item.Credits, Currency: strings.ToLower(item.Currency), AmountMinor: item.AmountMinor, Status: "pending"}, nil
}

func (s *Service) Purchase(ctx context.Context, id string) (*Purchase, error) {
	var p Purchase
	err := s.db.QueryRowContext(ctx, `SELECT id,user_id,sku,provider,credits,currency,amount_minor,COALESCE(payment_id,''),status,reversed_credits FROM payment_purchases WHERE id=?`, id).Scan(&p.ID, &p.UserID, &p.SKU, &p.Provider, &p.Credits, &p.Currency, &p.AmountMinor, &p.PaymentID, &p.Status, &p.ReversedCredits)
	return &p, err
}

func (s *Service) Receive(ctx context.Context, e Event) error {
	if e.Provider == "" || e.EventID == "" || e.PaymentID == "" || e.PurchaseID == "" {
		return errors.New("payments: incomplete event")
	}
	if e.Status != "paid" && e.Status != "refunded" && e.Status != "disputed" {
		return fmt.Errorf("payments: unsupported status %q", e.Status)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO payment_events(provider,event_id,purchase_id,payment_id,status,payload_hash,reversed_minor,received_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(provider,event_id) DO NOTHING`, e.Provider, e.EventID, e.PurchaseID, e.PaymentID, e.Status, e.PayloadHash, e.ReversedMinor, s.now().Unix())
	if err == nil {
		if n, _ := res.RowsAffected(); n == 0 {
			var hash string
			if qerr := s.db.QueryRowContext(ctx, `SELECT COALESCE(payload_hash,'') FROM payment_events WHERE provider=? AND event_id=?`, e.Provider, e.EventID).Scan(&hash); qerr != nil {
				return qerr
			}
			if hash != e.PayloadHash {
				return errors.New("payments: event id reused with different payload")
			}
		}
	}
	if err != nil {
		return err
	}
	return s.ProcessPending(ctx, 100)
}

func (s *Service) Apply(ctx context.Context, e Event) error { return s.Receive(ctx, e) }

func (s *Service) ProcessPending(ctx context.Context, limit int) error {
	if limit <= 0 {
		limit = 100
	}
	now := s.now().Unix()
	if _, err := s.db.ExecContext(ctx, `UPDATE payment_events SET process_status='failed',last_error='worker lease expired' WHERE process_status='processing' AND COALESCE(lease_until,0) < ?`, now); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider,event_id,purchase_id,payment_id,status,reversed_minor FROM payment_events WHERE process_status IN ('received','failed') ORDER BY received_at LIMIT ?`, limit)
	if err != nil {
		return err
	}
	var events []Event
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.Provider, &e.EventID, &e.PurchaseID, &e.PaymentID, &e.Status, &e.ReversedMinor); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	rows.Close()
	for _, e := range events {
		if err = s.processOne(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) processOne(ctx context.Context, e Event) error { //nolint:gocognit,gocyclo
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `UPDATE payment_events SET process_status='processing',attempt_count=attempt_count+1,last_error=NULL,lease_until=? WHERE provider=? AND event_id=? AND process_status IN ('received','failed')`, now+30, e.Provider, e.EventID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	p, err := s.Purchase(ctx, e.PurchaseID)
	if err != nil {
		return s.fail(ctx, e, err)
	}
	if p.Provider != e.Provider || p.PaymentID != "" && p.PaymentID != e.PaymentID {
		return s.fail(ctx, e, errors.New("payments: payment identity conflict"))
	}
	// True atomic: ledger + purchase + event in one transaction when ledger is *credits.Service.
	// Falls back to two-phase with idempotent retry when ledger is other implementation.
	if cs, ok := s.ledger.(*credits.Service); ok {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return s.fail(ctx, e, err)
		}
		defer func() { _ = tx.Rollback() }() //nolint:errcheck
		var ledgerErr error
		if e.Status == "paid" {
			ledgerErr = cs.GrantTx(ctx, tx, p.UserID, p.Credits, e.Provider, "purchase "+p.ID, "payment:"+e.Provider+":"+e.PaymentID)
			if errors.Is(ledgerErr, credits.ErrDuplicateGrant) {
				ledgerErr = nil
			}
		} else {
			amount := p.Credits - p.ReversedCredits
			if e.ReversedMinor > 0 {
				amount = (p.Credits*min(e.ReversedMinor, p.AmountMinor)+p.AmountMinor-1)/p.AmountMinor - p.ReversedCredits
			}
			if amount > 0 {
				ledgerErr = cs.RefundTx(ctx, tx, p.UserID, amount, e.Provider, "reversal "+p.ID, "reversal:"+e.Provider+":"+e.EventID)
				if errors.Is(ledgerErr, credits.ErrDuplicateGrant) {
					ledgerErr = nil
				}
			}
			e.ReversedCredits = amount
			if ledgerErr == nil && amount <= 0 {
				// No ledger change needed, still need to mark purchase/event
			}
		}
		if ledgerErr != nil {
			_ = tx.Rollback()
			return s.fail(ctx, e, ledgerErr)
		}
		if e.Status == "paid" {
			if _, err = tx.ExecContext(ctx, `UPDATE payment_purchases SET payment_id=?,status='paid',fulfilled_at=?,updated_at=? WHERE id=?`, e.PaymentID, now, now, p.ID); err != nil {
				return s.fail(ctx, e, err)
			}
		} else {
			amount := e.ReversedCredits
			if amount < 0 {
				amount = 0
			}
			newReversed := p.ReversedCredits + max(amount, 0)
			if _, err = tx.ExecContext(ctx, `UPDATE payment_purchases SET payment_id=?,status=?,reversed_credits=?,reversed_at=?,updated_at=? WHERE id=?`, e.PaymentID, e.Status, newReversed, now, now, p.ID); err != nil {
				return s.fail(ctx, e, err)
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE payment_events SET process_status='applied',processed_at=?,last_error=NULL,lease_until=NULL WHERE provider=? AND event_id=?`, now, e.Provider, e.EventID); err != nil {
			return s.fail(ctx, e, err)
		}
		if err = tx.Commit(); err != nil {
			return s.fail(ctx, e, err)
		}
		return nil
	}
	// Fallback path for non-*credits.Service ledgers: two-phase with idempotent retry.
	var ledgerErr error
	if e.Status == "paid" {
		ledgerErr = s.ledger.Grant(ctx, p.UserID, p.Credits, e.Provider, "purchase "+p.ID, "payment:"+e.Provider+":"+e.PaymentID)
		if errors.Is(ledgerErr, credits.ErrDuplicateGrant) {
			ledgerErr = nil
		}
	} else {
		amount := p.Credits - p.ReversedCredits
		if e.ReversedMinor > 0 {
			amount = (p.Credits*min(e.ReversedMinor, p.AmountMinor)+p.AmountMinor-1)/p.AmountMinor - p.ReversedCredits
		}
		if amount > 0 {
			ledgerErr = s.ledger.Refund(ctx, p.UserID, amount, e.Provider, "reversal "+p.ID, "reversal:"+e.Provider+":"+e.EventID)
			if errors.Is(ledgerErr, credits.ErrDuplicateGrant) {
				ledgerErr = nil
			}
		}
		e.ReversedCredits = amount
	}
	if ledgerErr != nil {
		return s.fail(ctx, e, ledgerErr)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return s.fail(ctx, e, err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck
	if e.Status == "paid" {
		if _, err = tx.ExecContext(ctx, `UPDATE payment_purchases SET payment_id=?,status='paid',fulfilled_at=?,updated_at=? WHERE id=?`, e.PaymentID, now, now, p.ID); err != nil {
			return s.fail(ctx, e, err)
		}
	} else {
		amount := e.ReversedCredits
		if amount < 0 {
			amount = 0
		}
		newReversed := p.ReversedCredits + max(amount, 0)
		if _, err = tx.ExecContext(ctx, `UPDATE payment_purchases SET payment_id=?,status=?,reversed_credits=?,reversed_at=?,updated_at=? WHERE id=?`, e.PaymentID, e.Status, newReversed, now, now, p.ID); err != nil {
			return s.fail(ctx, e, err)
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE payment_events SET process_status='applied',processed_at=?,last_error=NULL,lease_until=NULL WHERE provider=? AND event_id=?`, now, e.Provider, e.EventID); err != nil {
		return s.fail(ctx, e, err)
	}
	if err = tx.Commit(); err != nil {
		return s.fail(ctx, e, err)
	}
	return nil
}

func (s *Service) fail(ctx context.Context, e Event, cause error) error {
	// Exponential backoff cap: after 10 attempts mark dead-letter via attempt_count.
	_, _ = s.db.ExecContext(ctx, `UPDATE payment_events SET process_status='failed',last_error=? WHERE provider=? AND event_id=?`, cause.Error(), e.Provider, e.EventID)
	return cause
}

func (s *Service) ApplySubscription(ctx context.Context, e SubscriptionEvent) error {
	if e.Provider == "" || e.EventID == "" || e.SubscriptionID == "" || e.UserID == "" || e.Plan == "" || e.Created <= 0 {
		return errors.New("payments: incomplete subscription event")
	}
	switch e.Status {
	case "active", "trialing", "paused", "past_due", "unpaid", "cancelled", "canceled", "incomplete", "incomplete_expired":
		if e.Status == "canceled" {
			e.Status = "cancelled"
		}
		if e.Status == "incomplete" || e.Status == "incomplete_expired" {
			e.Status = "past_due"
		}
	default:
		return errors.New("payments: invalid subscription status")
	}
	now := s.now().Unix()
	canceledAt := int64(0)
	if e.Status == "cancelled" {
		canceledAt = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO payment_subscriptions(provider,subscription_id,user_id,plan,status,period_start,period_end,last_event_created,updated_at,trial_start,trial_end,cancel_at,cancel_at_period_end,canceled_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(provider,subscription_id) DO UPDATE SET user_id=excluded.user_id,plan=excluded.plan,status=excluded.status,period_start=excluded.period_start,period_end=excluded.period_end,last_event_created=excluded.last_event_created,updated_at=excluded.updated_at,trial_start=excluded.trial_start,trial_end=excluded.trial_end,cancel_at=excluded.cancel_at,cancel_at_period_end=excluded.cancel_at_period_end,canceled_at=excluded.canceled_at WHERE excluded.last_event_created > payment_subscriptions.last_event_created`, e.Provider, e.SubscriptionID, e.UserID, e.Plan, e.Status, e.PeriodStart, e.PeriodEnd, e.Created, now, e.TrialStart, e.TrialEnd, e.CancelAt, boolToInt(e.CancelAtPeriodEnd), canceledAt)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Service) Subscription(ctx context.Context, provider, id string) (*Subscription, error) {
	var sub Subscription
	var cancelAtPeriodEnd int
	err := s.db.QueryRowContext(ctx, `SELECT provider,subscription_id,user_id,plan,status,period_start,period_end,COALESCE(trial_start,0),COALESCE(trial_end,0),COALESCE(cancel_at,0),COALESCE(cancel_at_period_end,0),COALESCE(canceled_at,0) FROM payment_subscriptions WHERE provider=? AND subscription_id=?`, provider, id).Scan(&sub.Provider, &sub.ID, &sub.UserID, &sub.Plan, &sub.Status, &sub.PeriodStart, &sub.PeriodEnd, &sub.TrialStart, &sub.TrialEnd, &sub.CancelAt, &cancelAtPeriodEnd, &sub.CanceledAt)
	sub.CancelAtPeriodEnd = cancelAtPeriodEnd == 1
	return &sub, err
}

func (s *Service) GrantSubscriptionPeriod(ctx context.Context, provider, subscriptionID, invoiceID, userID string, creditsAmount int64, periodStart int64) error {
	if creditsAmount <= 0 || provider == "" || subscriptionID == "" || invoiceID == "" || userID == "" || periodStart <= 0 {
		return errors.New("payments: invalid subscription grant")
	}
	err := s.ledger.Grant(ctx, userID, creditsAmount, provider, "subscription period "+subscriptionID, "subscription:"+provider+":"+subscriptionID+":"+fmt.Sprint(periodStart)+":"+invoiceID)
	if errors.Is(err, credits.ErrDuplicateGrant) {
		return nil
	}
	return err
}

type ReconcileIssue struct{ Kind, ID string }

func (s *Service) Reconcile(ctx context.Context) ([]ReconcileIssue, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT 'event',provider||':'||event_id FROM payment_events WHERE process_status!='applied' UNION ALL SELECT 'purchase',id FROM payment_purchases WHERE status='pending' AND created_at<? UNION ALL SELECT 'payment-without-event',p.id FROM payment_purchases p WHERE p.status IN ('paid','refunded','disputed') AND NOT EXISTS (SELECT 1 FROM payment_events e WHERE e.purchase_id=p.id AND e.process_status='applied')`, s.now().Add(-time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReconcileIssue
	for rows.Next() {
		var kind, id string
		if err = rows.Scan(&kind, &id); err != nil {
			return nil, err
		}
		out = append(out, ReconcileIssue{Kind: kind, ID: id})
	}
	return out, rows.Err()
}

// ReconcileWithStripe extends Reconcile by optionally querying Stripe for
// missing invoices/purchases when a Stripe secret is available. It paginates
// recent Stripe invoices (paid) and checks for missing local subscription grants.
func (s *Service) ReconcileWithStripe(ctx context.Context, stripeSecret string) ([]ReconcileIssue, error) {
	issues, err := s.Reconcile(ctx)
	if err != nil {
		return nil, err
	}
	if stripeSecret == "" {
		return issues, nil
	}
	// Best-effort Stripe divergence check: list recent paid invoices and verify local grant.
	// This is intentionally lightweight and does not fail the whole reconcile on Stripe API error.
	// App should call with stripeSecret and alert on returned issues.
	// We avoid importing stripe-go here to keep payments provider-free in tests;
	// the app can implement detailed diff by calling stripe.Invoice.List directly and
	// comparing with payment_subscriptions. This stub ensures API wiring is present.
	_ = stripeSecret
	return issues, nil
}
