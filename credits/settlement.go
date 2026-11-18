package credits

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Service) EnqueueSettlement(ctx context.Context, requestID, userID, reservationID, provider, model string) error {
	now := s.cfg.Now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO settlement_outbox (request_id,user_id,reservation_id,provider,model,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(request_id) DO NOTHING`, requestID, userID, reservationID, provider, model, "pending", now, now)
	return err
}

func (s *Service) SettleViaOutbox(ctx context.Context, requestID string, usage Usage) error {
	var reservationID string
	var userID string
	var model string
	if err := s.db.QueryRowContext(ctx, `SELECT reservation_id,user_id,model FROM settlement_outbox WHERE request_id=?`, requestID).Scan(&reservationID, &userID, &model); err != nil {
		return err
	}
	resv, err := s.reservationByID(ctx, reservationID)
	if err != nil {
		return err
	}
	creditsCharged, err := s.Credits(ctx, usage)
	if err != nil {
		_ = s.Release(ctx, resv)
		_, _ = s.db.ExecContext(ctx, `UPDATE settlement_outbox SET status='failed',last_error=?,updated_at=? WHERE request_id=?`, err.Error(), s.cfg.Now().Unix(), requestID)
		return err
	}
	if err2 := s.Settle(ctx, resv, creditsCharged); err2 != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE settlement_outbox SET status='failed',last_error=?,attempt_count=attempt_count+1,updated_at=? WHERE request_id=?`, err2.Error(), s.cfg.Now().Unix(), requestID)
		return err2
	}
	usage.CostMicrounits, _ = s.Cost(ctx, usage)
	usage.CreditsCharged = creditsCharged
	_ = s.RecordUsageRetry(ctx, usage)
	_, err = s.db.ExecContext(ctx, `UPDATE settlement_outbox SET status='settled',updated_at=? WHERE request_id=?`, s.cfg.Now().Unix(), requestID)
	return err
}

// MarkOutboxSettled closes the outbox row a caller settled by another path.
//
// EnqueueSettlement writes a `pending` row before the request runs, so that a
// process which dies mid-request leaves the evidence a drainer needs. A caller
// that settles DIRECTLY (Settle + RecordUsage, which the biller does when the
// provider already returned real usage) still owes the row an outcome: without
// this, the drainer selects it, re-settles an already closed reservation, and
// records `failed` on a charge that was captured correctly. Every successful
// direct settlement then left a permanent phantom failure, re-selected on every
// drain.
//
// Idempotent and tolerant: an absent row is not an error, because not every
// settlement was enqueued (Settle does not require it) and a caller closing a
// row it never wrote is doing the right thing anyway. A row already `settled`
// stays settled; the update is conditional on `pending` so a late close cannot
// overwrite a drainer's verdict.
func (s *Service) MarkOutboxSettled(ctx context.Context, requestID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE settlement_outbox SET status='settled',updated_at=? WHERE request_id=? AND status='pending'`,
		s.cfg.Now().Unix(), requestID)
	return err
}

func (s *Service) reservationByID(ctx context.Context, id string) (*Reservation, error) {
	var r Reservation
	err := s.db.QueryRowContext(ctx, `SELECT id,user_id,request_id,amount,captured_amount,released_amount,status,created_at,updated_at FROM credit_reservations WHERE id=?`, id).Scan(&r.ID, &r.UserID, &r.RequestID, &r.Amount, &r.Captured, &r.Released, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		err = s.db.QueryRowContext(ctx, `SELECT id,user_id,request_id,amount,status,created_at,updated_at FROM credit_reservations WHERE id=?`, id).Scan(&r.ID, &r.UserID, &r.RequestID, &r.Amount, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	}
	return &r, err
}

// ProcessSettlementOutbox finishes settlements that were interrupted: it finds
// pending outbox rows that have recorded usage and settles them.
//
// WHAT THIS USED TO DO: select the pending request_ids, re-select the same rows,
// and expire anything older than an hour — never calling SettleViaOutbox and
// never reading usage. The drainer was a no-op: a row written by
// EnqueueSettlement stayed pending (or expired and released the reservation)
// while the request went unbilled. The shape looked like a recovery path, which
// is worse than having none, because a caller (like gogogo, draining every 30s)
// relies on it.
//
// The rule now: recorded usage is the evidence a charge is owed. With it, settle
// (settleOnce, so a concurrent drainer cannot double-charge). Without it, leave
// the row PENDING — the grace path below still applies, and marking it settled
// would claim a charge that never happened.
//
// Every timestamp comes from s.cfg.Now(), not time.Now(): the rest of this
// package is driven by the injected clock, and mixing the two made the grace
// window unpredictable under test (a test clock pinned in 2026 saw every fresh
// row as stale, because real time is later). That is a determinism bug on its
// own, independent of the missing settlement.
func (s *Service) ProcessSettlementOutbox(ctx context.Context, limit int) error {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT request_id FROM settlement_outbox WHERE status='pending' ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	const staleSettlementSeconds = int64(3600)
	now := s.cfg.Now().Unix()
	for _, id := range ids {
		u, ok, lookupErr := s.usageForRequest(ctx, id)
		if lookupErr != nil {
			return lookupErr
		}
		if ok {
			// Settle once: if another drainer got there first, that is success.
			if settleErr := s.settleOnce(ctx, id, u); settleErr != nil && !errors.Is(settleErr, ErrReservationClosed) {
				// Leave it pending for the next run and record why. Swallowing
				// this is how the no-op version stayed unnoticed.
				_, _ = s.db.ExecContext(ctx,
					`UPDATE settlement_outbox SET attempt_count=attempt_count+1,last_error=?,updated_at=? WHERE request_id=?`,
					settleErr.Error(), now, id)
			}
			continue
		}
		// No recorded usage: nothing to charge. Expire it after the grace
		// window so the reservation is not held forever, but never claim it
		// was settled.
		var created int64
		var reservationID string
		if err := s.db.QueryRowContext(ctx,
			`SELECT created_at, reservation_id FROM settlement_outbox WHERE request_id=?`, id).
			Scan(&created, &reservationID); err != nil {
			continue
		}
		if now-created <= staleSettlementSeconds {
			continue
		}
		// reservation_id, NOT request_id: they are different columns, and the
		// first version of this drainer passed the request id here, so the
		// lookup always failed and every stale row stayed pending.
		resv, err := s.reservationByID(ctx, reservationID)
		if err != nil {
			continue
		}
		if resv.Status == reservationStatusReserved {
			_ = s.Release(ctx, resv)
		}
		_, _ = s.db.ExecContext(ctx,
			`UPDATE settlement_outbox SET status='expired',last_error=?,updated_at=? WHERE request_id=?`,
			"no recorded usage within the grace window", now, id)
	}
	return nil
}

// usageForRequest rebuilds the Usage a pending settlement needs from the
// recorded llm_usage row. ok is false when nothing was recorded — the case that
// must stay pending rather than be settled blind.
func (s *Service) usageForRequest(ctx context.Context, requestID string) (u Usage, ok bool, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT request_id, user_id, provider, model, billing_mode,
		        input_tokens, output_tokens, cached_input_tokens, reasoning_tokens
		   FROM llm_usage WHERE request_id=?`, requestID)
	err = row.Scan(&u.RequestID, &u.UserID, &u.Provider, &u.Model, &u.BillingMode,
		&u.InputTokens, &u.OutputTokens, &u.CachedTokens, &u.ReasoningTokens)
	if errors.Is(err, sql.ErrNoRows) {
		return Usage{}, false, nil
	}
	if err != nil {
		return Usage{}, false, err
	}
	return u, true, nil
}

// settleOnce settles a request through the normal path. Re-running it is safe
// because the reservation's compare-and-set (see finalize) rejects a second
// finalization with ErrReservationClosed, which the caller treats as "already
// done".
func (s *Service) settleOnce(ctx context.Context, requestID string, u Usage) error {
	return s.SettleViaOutbox(ctx, requestID, u)
}
