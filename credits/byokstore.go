package credits

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
)

// BYOK credential storage. User-supplied provider credentials are sealed with
// XChaCha20-Poly1305 and stored in byok_credentials.
var (
	ErrCredentialStoreDisabled = errors.New("credits: byok store disabled (no sealing key)")
	ErrCredentialNotFound      = errors.New("credits: credential not found")
	ErrCredentialDecrypt       = errors.New("credits: credential decrypt failed")
)

func (s *Service) NewCredentialStore(encKey [32]byte) *CredentialStore {
	return &CredentialStore{db: s.db, blob: encKey, now: func() int64 { return s.cfg.Now().Unix() }}
}

func (c *CredentialStore) Put(ctx context.Context, userID, provider, cred string) error {
	if !c.keyed() {
		return ErrCredentialStoreDisabled
	}
	sealed, err := c.seal(cred)
	if err != nil {
		return err
	}
	ts := c.now()
	_, err = c.db.ExecContext(ctx,
		`INSERT INTO byok_credentials (user_id, provider, encrypted_key, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, provider) DO UPDATE SET
		   encrypted_key = excluded.encrypted_key, updated_at = excluded.updated_at`,
		userID, provider, sealed, ts, ts)
	return err
}

func (c *CredentialStore) Get(ctx context.Context, userID, provider string) (string, error) {
	if !c.keyed() {
		return "", ErrCredentialStoreDisabled
	}
	var sealed []byte
	err := c.db.QueryRowContext(ctx,
		`SELECT encrypted_key FROM byok_credentials WHERE user_id = ? AND provider = ?`,
		userID, provider).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrCredentialNotFound
	}
	if err != nil {
		return "", err
	}
	return c.open(sealed)
}

func (c *CredentialStore) Delete(ctx context.Context, userID, provider string) error {
	if !c.keyed() {
		return ErrCredentialStoreDisabled
	}
	_, err := c.db.ExecContext(ctx,
		`DELETE FROM byok_credentials WHERE user_id = ? AND provider = ?`,
		userID, provider)
	return err
}

func (c *CredentialStore) Configured(ctx context.Context, userID, provider string) (bool, error) {
	if !c.keyed() {
		return false, nil
	}
	var n int
	err := c.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM byok_credentials WHERE user_id = ? AND provider = ?`,
		userID, provider).Scan(&n)
	return n > 0, err
}

func (c *CredentialStore) keyed() bool {
	return c.blob != [32]byte{}
}

func (c *CredentialStore) seal(cred string) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(c.blob[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, []byte(cred), nil), nil
}

func (c *CredentialStore) open(sealed []byte) (string, error) {
	aead, err := chacha20poly1305.NewX(c.blob[:])
	if err != nil {
		return "", err
	}
	minLen := chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead
	if len(sealed) < minLen {
		return "", ErrCredentialDecrypt
	}
	nonce := sealed[:chacha20poly1305.NonceSizeX]
	ct := sealed[chacha20poly1305.NonceSizeX:]
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", errors.Join(ErrCredentialDecrypt, err)
	}
	return string(pt), nil
}
