package credits

import (
	"context"
	"strings"
)

// RecordUsage persists a single LLM call into the llm_usage table. It is
// append-only and keyed by request_id (unique). Pricing version and computed
// cost are captured at call time for later audit.
func (s *Service) RecordUsage(ctx context.Context, u Usage) error {
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
		// request_id UNIQUE: a duplicate call report is ignored (idempotent).
		if isUniqueViolation(err) {
			return nil
		}
		return err
	}
	return nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
