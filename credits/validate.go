package credits

import (
	"errors"
	"strings"
)

const maxIdentifierLength = 512

func requireIdentifier(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("credits: " + name + " is required")
	}
	if len(value) > maxIdentifierLength {
		return errors.New("credits: " + name + " is too long")
	}
	return nil
}

func validateUsage(u Usage) error {
	for name, value := range map[string]string{
		"request_id": u.RequestID,
		"user_id":    u.UserID,
		"provider":   u.Provider,
		"model":      u.Model,
	} {
		if err := requireIdentifier(name, value); err != nil {
			return err
		}
	}
	if u.BillingMode != billingModeManaged && u.BillingMode != billingModeByok {
		return errors.New("credits: invalid billing mode")
	}
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.CachedTokens < 0 || u.ReasoningTokens < 0 ||
		u.CostMicrounits < 0 || u.CreditsCharged < 0 {
		return errors.New("credits: usage values must be non-negative")
	}
	if u.BillingMode == billingModeByok && u.CreditsCharged != 0 {
		return errors.New("credits: byok usage cannot charge credits")
	}
	return nil
}
