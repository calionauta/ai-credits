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
