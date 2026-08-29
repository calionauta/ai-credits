package credits

import (
	"context"
	"testing"
)

// TestManagedBillingLifecycle covers the production managed-call boundary:
// grant -> reserve -> settle -> idempotent usage audit, preserving the ledger
// materialization invariant.
func TestManagedBillingLifecycle(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	if err := s.Grant(ctx, testUser, 100, "signup", "welcome", "signup:user_test"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Reserve(ctx, testUser, "managed:req-1", 30)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(ctx, r, 12); err != nil {
		t.Fatal(err)
	}
	u := Usage{RequestID: "managed:req-1", UserID: testUser, Provider: "goai", Model: "gpt-4o-mini", BillingMode: billingModeManaged, InputTokens: 100, OutputTokens: 50, CreditsCharged: 12}
	if err := s.RecordUsageRetry(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsageRetry(ctx, u); err != nil {
		t.Fatalf("usage retry must be idempotent: %v", err)
	}

	balance, err := s.Balance(ctx, testUser)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 88 {
		t.Fatalf("balance = %d, want 88", balance)
	}
	var ledger, usageRows int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount), 0) FROM credit_transactions WHERE user_id = ?`, testUser).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_usage WHERE request_id = ?`, u.RequestID).Scan(&usageRows); err != nil {
		t.Fatal(err)
	}
	if ledger != balance || usageRows != 1 {
		t.Fatalf("ledger=%d balance=%d usageRows=%d, want invariant and one audit row", ledger, balance, usageRows)
	}
}
