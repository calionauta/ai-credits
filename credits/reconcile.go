package credits

import (
	"context"
)

// Reconcile runs two maintenance tasks against the database:
//
//  1. Expire stale reservations (status=reserved older than the timeout),
//     returning their reserved amount to the balance via a reservation_release
//     ledger row — keeping the balance == SUM(ledger) invariant.
//  2. Report balance-vs-ledger mismatches (never auto-fix; drift implies an
//     external or buggy write and must be triaged by an operator).
//
// Returns the list of mismatches found (empty if healthy).
func (s *Service) Reconcile(ctx context.Context) ([]Mismatch, error) {
	if err := s.expireStale(ctx); err != nil {
		return nil, err
	}
	return s.detectDrift(ctx)
}

// expireStale marks reserved reservations older than cfg.ReservationTimeout
// as expired and returns their full reserved amount to the balance, recording
// a reservation_release ledger row for each.
func (s *Service) expireStale(ctx context.Context) error {
	cutoff := s.cfg.Now().Add(-s.cfg.ReservationTimeout).Unix()
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, amount FROM credit_reservations
		  WHERE status = ? AND created_at < ?`,
		reservationStatusReserved, cutoff)
	if err != nil {
		return err
	}
	type stale struct {
		id, userID string
		amount     int64
	}
	var stales []stale
	for rows.Next() {
		var st stale
		if err := rows.Scan(&st.id, &st.userID, &st.amount); err != nil {
			rows.Close()
			return err
		}
		stales = append(stales, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	now := s.cfg.Now().Unix()
	for _, st := range stales {
		if err := s.expireOne(ctx, st.id, st.userID, st.amount, now); err != nil {
			return err
		}
	}
	return nil
}

// expireOne expires a single stale reservation and returns its amount to the
// balance in one transaction. It is a no-op if a concurrent Settle/Release
// already finalized the reservation.
func (s *Service) expireOne(ctx context.Context, id, userID string, amount, now int64) error {
	tx, err := s.immediateTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Only flip + return when the reservation is still reserved.
	res, err := tx.ExecContext(ctx,
		`UPDATE credit_reservations SET status = ?, updated_at = ?
		  WHERE id = ? AND status = ?`,
		reservationStatusExpired, now, id, reservationStatusReserved)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // raced with settle/release; skip
	}
	lid, err := newID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO credit_transactions
		 (id, user_id, amount, type, source, reference_id, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, NULL, ?)`,
		lid, userID, amount, "reservation_release", "reconcile", id, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE credit_accounts SET balance = balance + ?, updated_at = ?
		  WHERE user_id = ?`,
		amount, now, userID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// detectDrift compares each account's materialized balance against the
// arithmetic sum of its ledger rows and reports any mismatch.
func (s *Service) detectDrift(ctx context.Context) ([]Mismatch, error) {
	rows, err := s.db.QueryContext(ctx,
		`WITH users AS (
		   SELECT user_id FROM credit_accounts
		   UNION SELECT user_id FROM credit_transactions
		 )
		 SELECT u.user_id, COALESCE(a.balance, 0),
		        COALESCE((SELECT SUM(t.amount) FROM credit_transactions t
		                   WHERE t.user_id = u.user_id), 0) AS ledger_sum
		   FROM users u LEFT JOIN credit_accounts a ON a.user_id = u.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Mismatch
	for rows.Next() {
		var m Mismatch
		if err := rows.Scan(&m.UserID, &m.Balance, &m.LedgerSum); err != nil {
			return nil, err
		}
		if m.Balance != m.LedgerSum {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}
