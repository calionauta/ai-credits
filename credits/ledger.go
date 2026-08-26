package credits

import (
	"context"
	"database/sql"
	"encoding/json"
)

// getBalance returns the current materialized balance for a user.
// Empty account (never granted) returns 0.
func (s *Service) Balance(ctx context.Context, userID string) (int64, error) {
	var bal int64
	err := s.db.QueryRowContext(ctx,
		`SELECT balance FROM credit_accounts WHERE user_id = ?`, userID).Scan(&bal)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return bal, err
}

// Grant credits the user by amount, recording a ledger entry atomically with
// the balance update inside a single transaction. Idempotent by idempotencyKey:
// a duplicate key returns ErrDuplicateGrant and does not change the balance.
// Grant never fails for a negative balance (see §6.4).
func (s *Service) Grant(ctx context.Context, userID string, amount int64,
	source, reason, idempotencyKey string,
) error {
	return s.adjust(ctx, userID, amount, source, reason, idempotencyKey, "grant")
}

// Refund debits credits (reduces balance) by amount, same idempotent ledger
// path as Grant, typed as a refund. Refund of already-spent credits may leave
// a negative balance; the ledger records the fact (see §6.4).
func (s *Service) Refund(ctx context.Context, userID string, amount int64,
	source, reason, idempotencyKey string,
) error {
	return s.adjust(ctx, userID, -amount, source, reason, idempotencyKey, "refund")
}

// adjust is the single atomic debit/credit path shared by Grant/Refund.
// It writes one ledger row and updates the materialized balance in one tx.
func (s *Service) adjust(ctx context.Context, userID string, amount int64,
	source, reason, idempotencyKey, typ string,
) error {
	tx, err := s.immediateTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Idempotency backstop: reserve the key inside the tx (UNIQUE constraint
	// is the final guard). Check-then-insert inside the tx, never check-then-insert outside.
	var exists int
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM credit_transactions WHERE idempotency_key = ?`,
		idempotencyKey).Scan(&exists)
	if err != nil {
		return err
	}
	if exists > 0 {
		return ErrDuplicateGrant
	}

	id, err := newID()
	if err != nil {
		return err
	}
	now := s.cfg.Now().Unix()
	meta, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return err
	}

	ri, err := tx.ExecContext(ctx,
		`INSERT INTO credit_transactions
		 (id, user_id, amount, type, source, idempotency_key, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, userID, amount, typ, source, idempotencyKey, string(meta), now)
	if err != nil {
		return err
	}
	if n, _ := ri.RowsAffected(); n == 0 {
		return ErrDuplicateGrant
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO credit_accounts (user_id, balance, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   balance = credit_accounts.balance + excluded.balance,
		   updated_at = excluded.updated_at`,
		userID, amount, now)
	if err != nil {
		return err
	}

	return tx.Commit()
}
