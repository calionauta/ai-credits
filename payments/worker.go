package payments

import (
	"context"
	"log/slog"
	"time"
)

// Worker runs ProcessPending in a loop with backoff and dead-letter.
// It is intended to be started from the app (gogogo/ensaiter) after Service init.
type Worker struct {
	svc            *Service
	interval       time.Duration
	batchSize      int
	maxAttempts    int
	deadLetterFunc func(ctx context.Context, e Event, err error)
	logger         *slog.Logger
}

type WorkerConfig struct {
	Interval    time.Duration
	BatchSize   int
	MaxAttempts int
	Logger      *slog.Logger
	// DeadLetter is called when an event exceeds MaxAttempts.
	DeadLetter func(ctx context.Context, e Event, err error)
}

func NewWorker(svc *Service, cfg WorkerConfig) *Worker {
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 10
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Worker{svc: svc, interval: cfg.Interval, batchSize: cfg.BatchSize, maxAttempts: cfg.MaxAttempts, deadLetterFunc: cfg.DeadLetter, logger: cfg.Logger}
}

// Run blocks until ctx cancels, processing pending events every interval with
// exponential backoff on failure (1s,2s,4s up to 30s) and dead-letter after maxAttempts.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.svc.ProcessPending(ctx, w.batchSize); err != nil {
				w.logger.Warn("payments: worker ProcessPending", "err", err, "backoff", backoff)
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
				// Check dead-letter threshold.
				w.checkDeadLetter(ctx)
				continue
			}
			backoff = time.Second
			w.checkDeadLetter(ctx)
		}
	}
}

func (w *Worker) checkDeadLetter(ctx context.Context) {
	if w.deadLetterFunc == nil {
		return
	}
	rows, err := w.svc.db.QueryContext(ctx, `SELECT provider,event_id,purchase_id,payment_id,status FROM payment_events WHERE process_status='failed' AND attempt_count >= ?`, w.maxAttempts)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Provider, &e.EventID, &e.PurchaseID, &e.PaymentID, &e.Status); err != nil {
			continue
		}
		w.logger.Error("payments: dead-letter", "provider", e.Provider, "event", e.EventID)
		w.deadLetterFunc(ctx, e, nil)
	}
}
