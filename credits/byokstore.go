package credits //nolint:lll,goconst

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
)

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
	if err := requireIdentifier("user_id", userID); err != nil {
		return err
	}
	if err := requireIdentifier("provider", provider); err != nil {
		return err
	}
	if cred == "" {
		return errors.New("credits: credential is required")
	}
	sealed, err := c.seal(cred, []byte(userID+"\x00"+provider))
	if err != nil {
		return err
	}
	ts := c.now()
	// Versioned: keep previous key on rotation for grace period.
	var prev []byte
	_ = c.db.QueryRowContext(ctx, `SELECT encrypted_key FROM byok_credentials WHERE user_id=? AND provider=?`, userID, provider).Scan(&prev)
	version := 1
	var curVer int
	if err2 := c.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM byok_credentials WHERE user_id=? AND provider=?`, userID, provider).Scan(&curVer); err2 == nil && curVer > 0 { //nolint:govet
		version = curVer + 1
	}
	_, err = c.db.ExecContext(ctx,
		`INSERT INTO byok_credentials (user_id, provider, encrypted_key, version, previous_key, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, provider) DO UPDATE SET
		   encrypted_key = excluded.encrypted_key, version = excluded.version, previous_key = excluded.previous_key, updated_at = excluded.updated_at`,
		userID, provider, sealed, version, prev, ts, ts)
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
	// Try current key, then previous version grace.
	cred, err := c.open(sealed, []byte(userID+"\x00"+provider))
	if err == nil {
		return cred, nil
	}
	// Fallback to previous_key if decrypt failed due to rotation race.
	var prev []byte
	if qerr := c.db.QueryRowContext(ctx, `SELECT previous_key FROM byok_credentials WHERE user_id=? AND provider=?`, userID, provider).Scan(&prev); qerr == nil && len(prev) > 0 {
		if pc, perr := c.open(prev, []byte(userID+"\x00"+provider)); perr == nil {
			return pc, nil
		}
	}
	return "", err
}

func (c *CredentialStore) GetVersion(ctx context.Context, userID, provider string) (int, error) {
	var v int
	err := c.db.QueryRowContext(ctx, `SELECT version FROM byok_credentials WHERE user_id=? AND provider=?`, userID, provider).Scan(&v)
	return v, err
}

func (c *CredentialStore) Rotate(ctx context.Context, userID, provider, newCred string) error {
	return c.Put(ctx, userID, provider, newCred)
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

func (c *CredentialStore) seal(cred string, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(c.blob[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, []byte(cred), aad), nil
}

func (c *CredentialStore) open(sealed, aad []byte) (string, error) {
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
	pt, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return "", errors.Join(ErrCredentialDecrypt, err)
	}
	return string(pt), nil
}
