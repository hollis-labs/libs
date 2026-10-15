package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// APIKey describes one static credential. Only the SHA-256 hex of the key is
// stored; use HashKey to produce it.
type APIKey struct {
	Hash      string // hex sha256 of the raw key
	ID        string
	Kind      string // default KindService
	Display   string
	Scopes    []string
	ExpiresAt time.Time // zero = never
}

// HashKey returns the hex SHA-256 of a raw API key.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// APIKeyVerifier is the static hashed-key backend (interim for Tesseract and
// root service credentials). Lookup is by hash, so the raw key is never
// compared directly.
type APIKeyVerifier struct {
	keys map[string]APIKey
	now  func() time.Time
}

// NewAPIKeyVerifier builds a verifier from keys.
func NewAPIKeyVerifier(keys ...APIKey) *APIKeyVerifier {
	v := &APIKeyVerifier{keys: make(map[string]APIKey, len(keys)), now: time.Now}
	for _, k := range keys {
		v.keys[k.Hash] = k
	}
	return v
}

// Verify implements Verifier.
func (v *APIKeyVerifier) Verify(_ context.Context, token string) (Principal, error) {
	k, ok := v.keys[HashKey(token)]
	if !ok || (!k.ExpiresAt.IsZero() && !v.now().Before(k.ExpiresAt)) {
		return Principal{}, ErrInvalidToken
	}
	kind := k.Kind
	if kind == "" {
		kind = KindService
	}
	return Principal{Verified: true, ID: k.ID, Kind: kind, Display: k.Display,
		Scopes: append([]string(nil), k.Scopes...), ExpiresAt: k.ExpiresAt}, nil
}

// Chain tries each verifier in order; the first success wins. An
// ErrInvalidToken from one moves on; if none accept and any returned another
// error, the result is ErrUnavailable, else ErrInvalidToken.
func Chain(vs ...Verifier) Verifier { return chain(vs) }

type chain []Verifier

func (c chain) Verify(ctx context.Context, token string) (Principal, error) {
	unavailable := false
	for _, v := range c {
		p, err := v.Verify(ctx, token)
		if err == nil {
			return p, nil
		}
		if !isInvalid(err) {
			unavailable = true
		}
	}
	if unavailable {
		return Principal{}, ErrUnavailable
	}
	return Principal{}, ErrInvalidToken
}
