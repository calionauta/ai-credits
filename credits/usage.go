package credits

import (
	"context"
	"strings"
	"time"
)

// RecordUsageRetry records usage and retries transient SQLite contention. Billing
// callers should use it after a successful settle: the ledger stays canonical,
// while this bounded retry preserves the audit trail under a busy sibling DB
// connection such as PocketBase.
func (s *Service) RecordUsageRetry(ctx context.Context, u Usage) error {
	const attempts = 3
	for attempt := range attempts {
		err := s.RecordUsage(ctx, u)
		if err == nil || !isBusyError(err) || attempt == attempts-1 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 25 * time.Millisecond):
		}
	}
	return nil
}

// RecordUsage persists a single LLM call into the llm_usage table. It is
// append-only and keyed by request_id (unique). Pricing version and computed
// cost are captured at call time for later audit.
func (s *Service) RecordUsage(ctx context.Context, u Usage) error {
	if err := validateUsage(u); err != nil {
		return err
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = s.cfg.Now()
	}
	id, err := newID()
	if err != nil {
		return err
	}
	version := u.PricingVersion
	if version == "" {
		version = s.pricer.version
	}
	var est *int64
	// CostMicrounits comes from the app via Cost(); capture it as-is.
	actual := u.CostMicrounits
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO llm_usage
		 (id, request_id, user_id, provider, model, billing_mode,
		  input_tokens, output_tokens, cached_input_tokens, reasoning_tokens,
		  estimated_cost_microunits, actual_cost_microunits, credits_charged,
		  pricing_version, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, u.RequestID, u.UserID, u.Provider, u.Model, u.BillingMode,
		u.InputTokens, u.OutputTokens, u.CachedTokens, u.ReasoningTokens,
		est, actual, u.CreditsCharged, version, u.CreatedAt.Unix())
	if err != nil {
		// request_id UNIQUE: only its expected constraint conflict is idempotent.
		if isUsageRequestDuplicate(err) {
			return nil
		}
		return err
	}
	return nil
}

func isUsageRequestDuplicate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: llm_usage.request_id")
}

func isBusyError(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "database is locked") ||
		strings.Contains(err.Error(), "database is busy"))
}
