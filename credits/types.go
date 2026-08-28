package credits

import (
	"errors"
	"time"
)

// Domain errors. All are sentinel errors wrapped by callers as needed.
var (
	// ErrInsufficientCredits means the user's balance is negative at reserve
	// time (per §6.4 the reserve fails when balance < 0, not just < amount).
	ErrInsufficientCredits = errors.New("credits: insufficient credits")
	// ErrReservationNotFound means the reservation id does not exist.
	ErrReservationNotFound = errors.New("credits: reservation not found")
	// ErrReservationClosed means the reservation is already settled or released.
	ErrReservationClosed = errors.New("credits: reservation already settled or released")
	// ErrDuplicateGrant means the idempotency key was already used.
	ErrDuplicateGrant = errors.New("credits: idempotency key already used")
	// ErrUnknownModel means the pricing table has no entry for the model.
	ErrUnknownModel = errors.New("credits: unknown model in pricing table")
	// ErrReservationExceeded existed when Settle refused to charge beyond the
	// reserved amount. Settle now auto-draws the deficit (reservation_overage)
	// instead, so this is kept only for callers that may still reference it; it
	// is no longer returned by the library.
	//
	// Deprecated: Settle auto-draws overage; this sentinel is never returned now.
	ErrReservationExceeded = errors.New("credits: actual cost exceeds reservation")
)

// Billing modes recorded in llm_usage.billing_mode.
const (
	billingModeManaged = "managed"
	billingModeByok    = "byok"
)

// Usage describes a single LLM call for RecordUsage.
type Usage struct {
	RequestID       string
	UserID          string
	Provider        string
	Model           string
	BillingMode     string // "managed" | "byok"
	InputTokens     int
	OutputTokens    int
	CachedTokens    int // cache-read tokens
	ReasoningTokens int
	CostMicrounits  int64 // filled by app via Cost(); 0 in BYOK if desired
	CreditsCharged  int64 // >0 in managed; 0 in BYOK
	PricingVersion  string
	CreatedAt       time.Time
}

// Result is returned by dynamic pricing helpers.
type Result struct {
	CostMicrounits int64
	Credits        int64
}

// Mismatch is a single balance-vs-ledger discrepancy reported by Reconcile.
type Mismatch struct {
	UserID    string
	LedgerSum int64
	Balance   int64
}
