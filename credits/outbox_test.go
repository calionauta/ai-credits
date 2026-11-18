package credits

import (
	"context"
	"testing"
)

// TestProcessSettlementOutboxSettlesStoredUsage pins the ONE thing the outbox
// exists for: a settlement that was interrupted (crash, dropped request) is
// finished by the drainer, not silently abandoned.
//
// ProcessSettlementOutbox used to select the pending request_ids, re-select the
// same rows, and only expire rows older than an hour — it never called
// SettleViaOutbox and never read usage. A row written by EnqueueSettlement was
// therefore never settled: the loop was a no-op that left the charge uncaptured
// until the 1h expiry released the reservation. Anyone reading the code saw a
// recovery mechanism that did not recover anything.
//
// RED-PROOFED: with the old drainer the status stays "pending" and the balance
// never moves.
func TestProcessSettlementOutboxSettlesStoredUsage(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k-outbox-settle"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Reserve(ctx, testUser, "req-rec", 100)
	if err != nil {
		t.Fatal(err)
	}
	// NOTE: the balance is a poor proxy here — settling REFUNDS the unused
	// reserve, so a correct charge still moves the balance UP. Assert on the
	// captured amount, which is the actual claim.
	// The process that would have settled died here. Its usage was recorded
	// (that is the whole point of record-then-settle), so the drainer has
	// everything it needs.
	usage := Usage{
		RequestID: "req-rec", UserID: testUser, Provider: "goai", Model: "gpt-4o-mini",
		BillingMode: billingModeManaged, InputTokens: 10, OutputTokens: 10,
	}
	if err := s.RecordUsage(ctx, usage); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	if err := s.EnqueueSettlement(ctx, "req-rec", testUser, r.ID, "goai", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}

	if err := s.ProcessSettlementOutbox(ctx, 10); err != nil {
		t.Fatalf("ProcessSettlementOutbox: %v", err)
	}

	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM settlement_outbox WHERE request_id=?`, "req-rec").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "settled" {
		t.Fatalf("outbox status = %q, want \"settled\": the drainer found recorded usage "+
			"for a pending settlement and left it uncharged — a recovery path that "+
			"recovers nothing", status)
	}

	var captured int64
	var rsvStatus string
	if err := s.db.QueryRowContext(ctx,
		`SELECT captured_amount, status FROM credit_reservations WHERE id=?`, r.ID).
		Scan(&captured, &rsvStatus); err != nil {
		t.Fatal(err)
	}
	if rsvStatus != reservationStatusCaptured || captured <= 0 {
		t.Fatalf("reservation status=%q captured=%d after the drainer ran, want status %q with "+
			"captured > 0: the interrupted request was never charged",
			rsvStatus, captured, reservationStatusCaptured)
	}
}

// TestProcessSettlementOutboxLeavesUnrecoverableRowsPending pins the honest
// half: without recorded usage there is nothing to charge, so the row must NOT
// be marked settled (that would claim a charge that never happened) and must
// not be expired early either — it still has its grace window, and the app may
// settle it directly with usage it holds in memory.
func TestProcessSettlementOutboxLeavesUnrecoverableRowsPending(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k-outbox-nousage"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Reserve(ctx, testUser, "req-nousage", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueSettlement(ctx, "req-nousage", testUser, r.ID, "goai", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}

	if err := s.ProcessSettlementOutbox(ctx, 10); err != nil {
		t.Fatalf("ProcessSettlementOutbox: %v", err)
	}

	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM settlement_outbox WHERE request_id=?`, "req-nousage").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want \"pending\": with no recorded usage nothing can be "+
			"charged, and marking it settled would claim a charge that never happened", status)
	}
}

// TestDirectSettleLeavesNoPhantomOutboxRow pins the interaction between the two
// settlement paths. SettleViaOutbox finalizes the row it settles; a caller that
// settles DIRECTLY (Settle + RecordUsage, which is what the gogogo biller does
// when it already has the real usage in hand) had no way to close the row
// EnqueueSettlement wrote a moment earlier.
//
// The damage is not theoretical: ProcessSettlementOutbox selects pending rows,
// whose stored usage now exists, so it calls SettleViaOutbox on an already
// settled reservation, gets an error, and stamps status='failed' on a charge
// that was captured correctly. Every successful request ended up holding a
// permanent "failed settlement" row, and the drainer re-selected it every minute
// forever. An operator looking for billing holes would find a wall of them.
func TestDirectSettleLeavesNoPhantomOutboxRow(t *testing.T) {
	s, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	if err := s.Grant(ctx, testUser, 1000, "signup", "w", "k-direct"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Reserve(ctx, testUser, "req-direct", 100)
	if err != nil {
		t.Fatal(err)
	}
	usage := Usage{
		RequestID: "req-direct", UserID: testUser, Provider: "goai", Model: "gpt-4o-mini",
		BillingMode: billingModeManaged, InputTokens: 10, OutputTokens: 10,
	}
	if err := s.EnqueueSettlement(ctx, "req-direct", testUser, r.ID, "goai", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	// The direct path: real usage is in hand, settle now.
	charged, costErr := s.Credits(ctx, usage)
	if costErr != nil {
		t.Fatalf("Credits: %v", costErr)
	}
	if err := s.Settle(ctx, r, charged); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	usage.CreditsCharged = charged
	if err := s.RecordUsage(ctx, usage); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	// Closing the row is the caller's obligation, and it must not need a second
	// round trip that re-reads the reservation it just settled.
	if err := s.MarkOutboxSettled(ctx, "req-direct"); err != nil {
		t.Fatalf("MarkOutboxSettled: %v", err)
	}

	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM settlement_outbox WHERE request_id=?`, "req-direct").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "settled" {
		t.Fatalf("outbox status = %q, want \"settled\"", status)
	}

	// The drainer must now find nothing to do. Before the row could be closed,
	// it stamped the settled charge as 'failed' here.
	if drainErr := s.ProcessSettlementOutbox(ctx, 10); drainErr != nil {
		t.Fatalf("ProcessSettlementOutbox: %v", drainErr)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM settlement_outbox WHERE request_id=?`, "req-direct").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "settled" {
		t.Fatalf("after draining, outbox status = %q, want \"settled\": the drainer "+
			"re-opened a charge that had already been captured", status)
	}
}
