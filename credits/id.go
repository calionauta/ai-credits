package credits

import (
	"crypto/rand"
	"encoding/hex"
)

// randomIDBytes is the length of newID() output (128 bits).
const randomIDBytes = 16

// newID returns a random 16-byte hex id (32 chars). Used for ledger rows,
// reservations, and usage rows. crypto/rand never blocks in practice.
func newID() (string, error) {
	b := make([]byte, randomIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// NewRequestID returns a fresh random id for a usage/reservation/lookup key
// (e.g. "req:<id>"). Wraps newID — the one place goai-less call sites get an
// idempotency-scoped id without importing uuid themselves.
func NewRequestID() string {
	id, err := newID()
	if err != nil {
		// crypto/rand failing means the process is broken; a fallback keeps
		// callers simple (they never see an error). Collisions are ~impossible.
		panic("credits: crypto/rand unavailable: " + err.Error())
	}
	return id
}
