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
	ReversedCredits                                               int64
	OccurredAt                                                    time.Time
}

type Purchase struct {
	ID, UserID, SKU, Provider, Currency, PaymentID, Status string
	Credits, AmountMinor, ReversedCredits                  int64
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO payment_events(provider,event_id,purchase_id,payment_id,status,payload_hash,received_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(provider,event_id) DO NOTHING`, e.Provider, e.EventID, e.PurchaseID, e.PaymentID, e.Status, e.PayloadHash, s.now().Unix())
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
	rows, err := s.db.QueryContext(ctx, `SELECT provider,event_id,purchase_id,payment_id,status FROM payment_events WHERE process_status IN ('received','failed') ORDER BY received_at LIMIT ?`, limit)
	if err != nil {
		return err
	}
	var events []Event
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.Provider, &e.EventID, &e.PurchaseID, &e.PaymentID, &e.Status); err != nil {
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

func (s *Service) processOne(ctx context.Context, e Event) error {
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `UPDATE payment_events SET process_status='processing',attempt_count=attempt_count+1,last_error=NULL WHERE provider=? AND event_id=? AND process_status IN ('received','failed')`, e.Provider, e.EventID)
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
	if e.Status == "paid" {
		err = s.ledger.Grant(ctx, p.UserID, p.Credits, e.Provider, "purchase "+p.ID, "payment:"+e.Provider+":"+e.PaymentID)
		if errors.Is(err, credits.ErrDuplicateGrant) {
			err = nil
		}
		if err == nil {
			_, err = s.db.ExecContext(ctx, `UPDATE payment_purchases SET payment_id=?,status='paid',fulfilled_at=?,updated_at=? WHERE id=?`, e.PaymentID, now, now, p.ID)
		}
	} else {
		amount := p.Credits - p.ReversedCredits
		if amount > 0 {
			err = s.ledger.Refund(ctx, p.UserID, amount, e.Provider, "reversal "+p.ID, "reversal:"+e.Provider+":"+e.PaymentID)
			if errors.Is(err, credits.ErrDuplicateGrant) {
				err = nil
			}
		}
		if err == nil {
			_, err = s.db.ExecContext(ctx, `UPDATE payment_purchases SET payment_id=?,status=?,reversed_credits=credits,reversed_at=?,updated_at=? WHERE id=?`, e.PaymentID, e.Status, now, now, p.ID)
		}
	}
	if err != nil {
		return s.fail(ctx, e, err)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE payment_events SET process_status='applied',processed_at=?,last_error=NULL WHERE provider=? AND event_id=?`, now, e.Provider, e.EventID)
	return err
}

func (s *Service) fail(ctx context.Context, e Event, cause error) error {
	_, _ = s.db.ExecContext(ctx, `UPDATE payment_events SET process_status='failed',last_error=? WHERE provider=? AND event_id=?`, cause.Error(), e.Provider, e.EventID)
	return cause
}

type ReconcileIssue struct{ Kind, ID string }

func (s *Service) Reconcile(ctx context.Context) ([]ReconcileIssue, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider||':'||event_id FROM payment_events WHERE process_status!='applied' UNION ALL SELECT id FROM payment_purchases WHERE status='pending' AND created_at<?`, s.now().Add(-time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReconcileIssue
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, ReconcileIssue{Kind: "pending", ID: id})
	}
	return out, rows.Err()
}
