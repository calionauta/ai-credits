package payments

import (
	"context"
	"database/sql"
	"github.com/calionauta/ai-credits/credits"
	_ "modernc.org/sqlite"
	"testing"
)

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
