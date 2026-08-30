package credits

import (
	"context"
	"database/sql"
	"time"
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
	if err := s.Settle(ctx, resv, creditsCharged); err != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE settlement_outbox SET status='failed',last_error=?,attempt_count=attempt_count+1,updated_at=? WHERE request_id=?`, err.Error(), s.cfg.Now().Unix(), requestID)
		return err
	}
	usage.CostMicrounits, _ = s.Cost(ctx, usage)
	usage.CreditsCharged = creditsCharged
	_ = s.RecordUsageRetry(ctx, usage)
	_, err = s.db.ExecContext(ctx, `UPDATE settlement_outbox SET status='settled',updated_at=? WHERE request_id=?`, s.cfg.Now().Unix(), requestID)
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

func (s *Service) ProcessSettlementOutbox(ctx context.Context, limit int) error {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT request_id FROM settlement_outbox WHERE status='pending' ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		// Attempt to settle via stored usage if available; otherwise expire stale pending
		var resID string
		var created int64
		var provider, model string
		if err := s.db.QueryRowContext(ctx, `SELECT reservation_id,created_at,provider,model FROM settlement_outbox WHERE request_id=?`, id).Scan(&resID, &created, &provider, &model); err != nil {
			continue
		}
		if time.Now().Unix()-created > 3600 {
			if resv, err := s.reservationByID(ctx, resID); err == nil && resv.Status == "reserved" {
				_ = s.Release(context.Background(), resv)
				_, _ = s.db.ExecContext(ctx, `UPDATE settlement_outbox SET status='expired',updated_at=? WHERE request_id=?`, s.cfg.Now().Unix(), id)
			}
		} else {
			// Retry pending with generic usage lookup would go here; for now keep pending for explicit SettleViaOutbox call
		}
		_ = provider
		_ = model
	}
	return nil
}

func (s *Service) ensureSettlementSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS settlement_outbox (request_id TEXT PRIMARY KEY,user_id TEXT NOT NULL,reservation_id TEXT NOT NULL,provider TEXT NOT NULL,model TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'pending',attempt_count INTEGER NOT NULL DEFAULT 0,last_error TEXT,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL)`)
	return err
}

var _ = sql.ErrNoRows
