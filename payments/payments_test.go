package payments

import (
	"context"
	"database/sql"
	"github.com/calionauta/ai-credits/credits"
	_ "modernc.org/sqlite"
	"testing"
)

func TestWorkerRecoversExpiredProcessingLease(t *testing.T) {
	db, err := sql.Open("sqlite", "file:restart?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := credits.New(db, credits.Config{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(db, ledger, map[string]CatalogItem{"topup": {Credits: 100, Currency: "usd", AmountMinor: 999}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, err := svc.CreatePurchase(ctx, "stripe", "u", "topup")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO payment_events(provider,event_id,purchase_id,payment_id,status,process_status,received_at,lease_until) VALUES('stripe','evt_restart',?,'pi_restart','paid','processing',1,1)`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.ProcessPending(ctx, 10); err != nil {
		t.Fatal(err)
	}
	balance, err := ledger.Balance(ctx, "u")
	if err != nil || balance != 100 {
		t.Fatalf("balance=%d err=%v", balance, err)
	}
	var status string
	if err = db.QueryRow(`SELECT process_status FROM payment_events WHERE event_id='evt_restart'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "applied" {
		t.Fatalf("status=%s", status)
	}
}

func TestPurchaseLifecycle(t *testing.T) {
	db, err := sql.Open("sqlite", "file:test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l, err := credits.New(db, credits.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(db, l, map[string]CatalogItem{"topup": {Credits: 100, Currency: "usd", AmountMinor: 999}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, err := s.CreatePurchase(ctx, "stripe", "u", "topup")
	if err != nil {
		t.Fatal(err)
	}
	e := Event{Provider: "stripe", EventID: "evt_1", PaymentID: "pi_1", PurchaseID: p.ID, Status: "paid"}
	if err = s.Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	b, _ := l.Balance(ctx, "u")
	if b != 100 {
		t.Fatalf("balance %d", b)
	}
	if err = s.Apply(ctx, Event{Provider: "stripe", EventID: "evt_2", PaymentID: "pi_1", PurchaseID: p.ID, Status: "refunded"}); err != nil {
		t.Fatal(err)
	}
	b, _ = l.Balance(ctx, "u")
	if b != 0 {
		t.Fatalf("refund balance %d", b)
	}
}
