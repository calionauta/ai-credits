package credits

import (
	"context"
	"database/sql"
	"errors"
)

// Reserved currently withdrawn from the user's balance awaiting settlement.
const (
	reservationStatusReserved = "reserved"
	reservationStatusCaptured = "captured"
	reservationStatusReleased = "released"
	reservationStatusExpired  = "expired"
)

// Reservation is a held amount for an unknown-output LLM call.
type Reservation struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	RequestID string `json:"request_id"`
	Amount    int64  `json:"amount"`
	Captured  int64  `json:"captured"`
	Released  int64  `json:"released"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// Reserve withdraws amount (>=1) from the user's balance for a pending call.
// It is idempotent by requestID: a second Reserve with the same requestID
// returns the existing reservation (not a new one).
//
// Per §6.4 the reserve fails with ErrInsufficientCredits when balance < 0
// (not merely < amount), matching the "balance can go negative only via
// grant/refund" rule.
func (s *Service) Reserve(ctx context.Context, userID, requestID string, amount int64) (*Reservation, error) {
	if amount < 1 {
		return nil, errors.New("credits: reservation amount must be >= 1")
	}
	tx, err := s.immediateTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit or error return

	// Idempotency: return the existing reservation if requestID already used.
	existing, err := s.lookupReservation(ctx, tx, requestID)
	switch {
	case err == nil:
		if existing.UserID != userID {
			return nil, errors.New("credits: request_id already used by another user")
		}
		if err2 := tx.Commit(); err2 != nil {
			return nil, err2
		}
		return existing, nil
	case errors.Is(err, sql.ErrNoRows):
		// fallthrough: fresh reservation below
	default:
		return nil, err
	}

	now := s.cfg.Now().Unix()

	// Anti-negative guard in One atomic statement: only reserve if the new
	// balance would stay >= 0.
	rid, err := newID()
	if err != nil {
		return nil, err
	}

	// Debiting + inserting the reservation + its ledger row is one atomic
	// tx; extracted for cyclomatic-complexity hygiene.
	if err := s.debitReservation(ctx, tx, rid, userID, requestID, amount, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Reservation{
		ID: rid, UserID: userID, RequestID: requestID, Amount: amount,
		Status: reservationStatusReserved, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// debitReservation withdraws amount from the balance (anti-negative guard),
// inserts the reservation row, and records the reserve debit ledger row.
func (s *Service) debitReservation(ctx context.Context, tx *sql.Tx, rid, userID, requestID string,
	amount int64, now int64,
) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE credit_accounts
		    SET balance = balance - ?, updated_at = ?
		  WHERE user_id = ? AND balance - ? >= 0`,
		amount, now, userID, amount)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrInsufficientCredits
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO credit_reservations
		 (id, user_id, request_id, amount, captured_amount, released_amount,
		  status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 0, 0, ?, ?, ?)`,
		rid, userID, requestID, amount, reservationStatusReserved, now, now)
	if err != nil {
		return err
	}
	// Keep the ledger invariant (balance == SUM(ledger)): the reserve debit
	// is a ledger row type=reservation referencing the reservation id.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO credit_transactions
		 (id, user_id, amount, type, source, reference_id, idempotency_key, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		rid+"-x", userID, -amount, "reservation", "llm_reserve", rid, "resv:"+requestID, now)
	return err
}

// lookupReservation returns the existing reservation for a requestID, or
// sql.ErrNoRows if none exists.
func (s *Service) lookupReservation(ctx context.Context, tx *sql.Tx, requestID string) (*Reservation, error) {
	var existing Reservation
	err := tx.QueryRowContext(ctx,
		`SELECT id, user_id, request_id, amount, captured_amount, released_amount,
		        status, created_at, updated_at
		   FROM credit_reservations WHERE request_id = ?`, requestID).
		Scan(&existing.ID, &existing.UserID, &existing.RequestID, &existing.Amount,
			&existing.Captured, &existing.Released, &existing.Status,
			&existing.CreatedAt, &existing.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &existing, nil
}

// Settle finalizes a reservation at the actual cost (in credits, <= Amount).
// The excess reserved credits are returned to the balance via a ledger
// reservation_release row. Settling for MORE than reserved is rejected with
// ErrReservationExceeded — never silently clamped (critique G1/G2).
func (s *Service) Settle(ctx context.Context, r *Reservation, actualCredits int64) error {
	if r == nil {
		return ErrReservationNotFound
	}
	return s.finalize(ctx, r, actualCredits, false)
}

// Release returns the entire reserved amount to the balance without capturing
// any (call failed/aborted).
func (s *Service) Release(ctx context.Context, r *Reservation) error {
	if r == nil {
		return ErrReservationNotFound
	}
	return s.finalize(ctx, r, 0, true)
}

func (s *Service) finalize(ctx context.Context, r *Reservation, captured int64, releaseAll bool) error {
	if !releaseAll && captured < 0 {
		return errors.New("credits: negative capture")
	}
	out := s.outcome(r, captured, releaseAll)
	if out.err != nil {
		return out.err
	}

	tx, err := s.immediateTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Re-read the reservation under the tx to guard against a concurrent
	// Settle/Release racing in.
	var dbStatus string
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM credit_reservations WHERE id = ?`, r.ID).Scan(&dbStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrReservationNotFound
	}
	if err != nil {
		return err
	}
	if dbStatus != reservationStatusReserved {
		return ErrReservationClosed
	}

	now := s.cfg.Now().Unix()
	if out.returnAmt > 0 {
		werr := s.writeReturn(ctx, tx, r, out.returnAmt, out.ledType, out.ledSource, now)
		if werr != nil {
			return werr
		}
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE credit_reservations
		    SET captured_amount = ?, released_amount = ?, status = ?, updated_at = ?
		  WHERE id = ?`,
		captured, out.returnAmt, out.status, now, r.ID)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Reflect the settled state back into the caller's struct.
	r.Captured = captured
	r.Released = out.returnAmt
	r.Status = out.status
	r.UpdatedAt = now
	return nil
}

// outcome decides the settle/release result for a reservation.
type outcome struct {
	returnAmt int64
	status    string
	ledType   string
	ledSource string
	err       error
}

func (s *Service) outcome(r *Reservation, captured int64, releaseAll bool) outcome {
	if !releaseAll && captured > r.Amount {
		return outcome{err: ErrReservationExceeded}
	}
	if r.Status != reservationStatusReserved {
		return outcome{err: ErrReservationClosed}
	}
	if releaseAll {
		return outcome{
			returnAmt: r.Amount, status: reservationStatusReleased,
			ledType: "reservation_release", ledSource: "llm_cancel",
		}
	}
	out := outcome{status: reservationStatusCaptured, ledType: "reservation_release", ledSource: "llm_settle"}
	out.returnAmt = r.Amount - captured
	return out
}

// writeReturn records the ledger row + balance bump returning unused credits.
func (s *Service) writeReturn(ctx context.Context, tx *sql.Tx, r *Reservation,
	amount int64, ledType, ledSource string, now int64,
) error {
	lid, err := newID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO credit_transactions
		 (id, user_id, amount, type, source, reference_id, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, NULL, ?)`,
		lid, r.UserID, amount, ledType, ledSource, r.ID, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE credit_accounts SET balance = balance + ?, updated_at = ?
		  WHERE user_id = ?`,
		amount, now, r.UserID)
	return err
}
