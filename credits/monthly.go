package credits

import (
	"context"
	"errors"
)

// monthlyGrantKey returns the idempotency key for a monthly grant period
// (YYYY-MM UTC). Used to make EnsureMonthlyGrant idempotent in the ledger.
func monthlyGrantKey(userID, period string) string {
	return "monthly:" + userID + ":" + period
}

// planCreditFor returns the monthly credit amount for a user's plan, falling
// back to Config.MonthlyCredits when the plan has no specific entry.
func (s *Service) planCreditFor(plan string) int64 {
	if plan != "" && s.cfg.PlanMonthlyCredits != nil {
		if v, ok := s.cfg.PlanMonthlyCredits[plan]; ok {
			return v
		}
	}
	return s.cfg.MonthlyCredits
}

// EnsureMonthlyGrant lazily grants the configured monthly credits for the
// current period, exactly once per user per period. It is idempotent (the
// grant's ledger idempotency key is monthly:<user>:<YYYY-MM>), so it is safe
// to call on every request — no scheduler, no period tracking.
//
// Returns (granted, error): granted is true only when a new grant was written.
//
// Entitlement gate: when a subscription record exists for the user and its
// status is not SubscriptionActive, the grant is refused (billing paused/
// cancelled). Users with no subscription record — prepaid-only apps, or
// callers that never use SetSubscription — are unaffected.
func (s *Service) EnsureMonthlyGrant(ctx context.Context, userID, plan string) (bool, error) {
	// Entitlement gate: refuse to mint when a subscription is not active.
	sub, err := s.Subscription(ctx, userID)
	switch {
	case err == nil && sub.Status != SubscriptionActive:
		return false, nil // paused/cancelled subscription: stop granting
	case err != nil && !errors.Is(err, ErrSubscriptionNotFound):
		return false, err
	}

	amount := s.planCreditFor(plan)
	if amount <= 0 {
		return false, nil // monthly grants disabled for this plan
	}
	period := s.cfg.Now().UTC().Format("2006-01")
	key := monthlyGrantKey(userID, period)
	err = s.Grant(ctx, userID, amount, "monthly", "monthly_grant", key)
	if errors.Is(err, ErrDuplicateGrant) {
		return false, nil // already granted this period
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Period returns the current monthly period (YYYY-MM) as derived from the
// injected clock, useful for app-side UI labels.
func (s *Service) Period() string {
	return s.cfg.Now().UTC().Format("2006-01")
}
