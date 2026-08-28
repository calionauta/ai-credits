package credits

import (
	"context"
	"database/sql"
	"errors"
)

// Subscription statuses. A subscription is an OPTIONAL entitlement gate over
// the lazy monthly grant: when a subscription record exists and is not
// "active", EnsureMonthlyGrant stops minting credits for that user — the
// billing side of cancelling/​pausing a plan. Without a subscription record
// (typical for prepaid-only apps), the grant stays unconditional, so this is
// fully backward-compatible.
const (
	SubscriptionActive   = "active"
	SubscriptionPaused   = "paused"
	SubscriptionCanceled = "cancelled"
)

var ErrSubscriptionNotFound = errors.New("credits: subscription not found")

// Subscription is a user's plan subscription status.
type Subscription struct {
	UserID    string
	Plan      string
	Status    string
	CreatedAt int64
	UpdatedAt int64
}

// SetSubscription creates or updates a user's subscription status. When a
// plan is set to anything other than active, the monthly grant gate blocks
// new credits (existing balance is untouched and reusable).
func (s *Service) SetSubscription(ctx context.Context, userID, plan, status string) error {
	switch status {
	case SubscriptionActive, SubscriptionPaused, SubscriptionCanceled:
	default:
		return errors.New("credits: invalid subscription status")
	}
	now := s.cfg.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscriptions (user_id, plan, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   plan = excluded.plan, status = excluded.status, updated_at = excluded.updated_at`,
		userID, plan, status, now, now)
	return err
}

// CancelSubscription stops the user's subscription (entitlement gate closes).
// It is a shorthand for SetSubscription(..., "cancelled"); kept separate so
// callers read intent. Existing balance remains usable.
func (s *Service) CancelSubscription(ctx context.Context, userID string) error {
	sub, err := s.Subscription(ctx, userID)
	if err != nil {
		return err
	}
	return s.SetSubscription(ctx, userID, sub.Plan, SubscriptionCanceled)
}

// Subscription returns the user's current subscription, or
// ErrSubscriptionNotFound if none has ever been recorded.
func (s *Service) Subscription(ctx context.Context, userID string) (*Subscription, error) {
	var sub Subscription
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, plan, status, created_at, updated_at
		   FROM subscriptions WHERE user_id = ?`, userID).
		Scan(&sub.UserID, &sub.Plan, &sub.Status, &sub.CreatedAt, &sub.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sub, nil
}
