package credits

import (
	"context"
	"testing"
	"time"
)

func TestSettlementOutboxGrace(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k1"); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Reserve(ctx, testUser, "req-outbox", 100)
	if err := s.EnqueueSettlement(ctx, "req-outbox", testUser, r.ID, "goai", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	u := Usage{RequestID: "req-outbox", UserID: testUser, Provider: "goai", Model: "gpt-4o-mini", BillingMode: billingModeManaged, InputTokens: 10, OutputTokens: 10}
	if err := s.SettleViaOutbox(ctx, "req-outbox", u); err != nil {
		t.Fatalf("SettleViaOutbox: %v", err)
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM settlement_outbox WHERE request_id=?`, "req-outbox").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "settled" {
		t.Fatalf("status=%s want settled", status)
	}
	// stale pending should expire after 1h
	r2, _ := s.Reserve(ctx, testUser, "req-stale", 50)
	_ = s.EnqueueSettlement(ctx, "req-stale", testUser, r2.ID, "goai", "gpt-4o-mini")
	// fake old created_at
	_, _ = s.db.ExecContext(ctx, `UPDATE settlement_outbox SET created_at=? WHERE request_id=?`, time.Now().Add(-2*time.Hour).Unix(), "req-stale")
	if err := s.ProcessSettlementOutbox(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM settlement_outbox WHERE request_id=?`, "req-stale").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "expired" {
		t.Fatalf("stale should expire, got %s", status)
	}
}

func TestByokVersionGrace(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()
	k1 := [32]byte{}
	for i := range k1 {
		k1[i] = byte(i)
	}
	store := s.NewCredentialStore(k1)
	if err := store.Put(ctx, testUser, "openai", "sk-1"); err != nil {
		t.Fatal(err)
	}
	v1, _ := store.GetVersion(ctx, testUser, "openai")
	if v1 != 1 {
		t.Fatalf("v1=%d", v1)
	}
	if err := store.Rotate(ctx, testUser, "openai", "sk-2"); err != nil {
		t.Fatal(err)
	}
	v2, _ := store.GetVersion(ctx, testUser, "openai")
	if v2 != 2 {
		t.Fatalf("v2=%d", v2)
	}
	cred, _ := store.Get(ctx, testUser, "openai")
	if cred != "sk-2" {
		t.Fatalf("cred=%s", cred)
	}
}
