package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// JWTConfig configures a JWTVerifier.
type JWTConfig struct {
	Issuer   string // required "iss" value; empty skips the check
	Audience string // required "aud" (one app id per token)
	JWKSURL  string // issuer's /.well-known/jwks.json
	// BootstrapKeyFile is a JWKS JSON file or a PEM Ed25519 public key used
	// until the first successful JWKS fetch (issuer unreachable at start). A
	// PEM key carries no kid and is accepted for any token kid.
	BootstrapKeyFile string
	Client           *http.Client
	RefreshInterval  time.Duration // periodic refresh in Run; default 5m
	MinRefresh       time.Duration // floor between on-demand refetches; default 10s
	Leeway           time.Duration // clock skew for exp/nbf; default 30s
	DenyList         *DenyList     // optional revocation check
	Now              func() time.Time
}

// JWTVerifier verifies Ed25519 (EdDSA) JWTs offline against cached JWKS keys.
type JWTVerifier struct {
	cfg JWTConfig

	mu        sync.Mutex
	keys      map[string]ed25519.PublicKey // by kid; "" = any-kid bootstrap key
	fetched   bool                         // a JWKS fetch has succeeded
	lastFetch time.Time
}

// NewJWTVerifier validates cfg and loads the bootstrap key, if any. It makes
// no network call; keys are fetched lazily and by Run.
func NewJWTVerifier(cfg JWTConfig) (*JWTVerifier, error) {
	if cfg.Audience == "" {
		return nil, errors.New("auth: JWTConfig.Audience is required")
	}
	if cfg.JWKSURL == "" && cfg.BootstrapKeyFile == "" {
		return nil, errors.New("auth: JWTConfig needs JWKSURL or BootstrapKeyFile")
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 5 * time.Minute
	}
	if cfg.MinRefresh <= 0 {
		cfg.MinRefresh = 10 * time.Second
	}
	if cfg.Leeway <= 0 {
		cfg.Leeway = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	v := &JWTVerifier{cfg: cfg, keys: map[string]ed25519.PublicKey{}}
	if cfg.BootstrapKeyFile != "" {
		b, err := os.ReadFile(cfg.BootstrapKeyFile)
		if err != nil {
			return nil, fmt.Errorf("auth: read bootstrap key: %w", err)
		}
		keys, err := parseKeyFile(b)
		if err != nil {
			return nil, fmt.Errorf("auth: bootstrap key: %w", err)
		}
		v.keys = keys
	}
	return v, nil
}

// Run refreshes the JWKS every RefreshInterval until ctx is done.
func (v *JWTVerifier) Run(ctx context.Context) {
	if v.cfg.JWKSURL == "" {
		return
	}
	t := time.NewTicker(v.cfg.RefreshInterval)
	defer t.Stop()
	for {
		_ = v.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Refresh fetches the JWKS now and replaces the key set (rotated-out keys
// disappear). On failure the cached keys are kept.
func (v *JWTVerifier) Refresh(ctx context.Context) error {
	if v.cfg.JWKSURL == "" {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.refreshLocked(ctx)
}

func (v *JWTVerifier) refreshLocked(ctx context.Context) error {
	v.lastFetch = v.cfg.Now()
	// JWKSURL is operator configuration, not request input.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil) // #nosec G704
	if err != nil {
		return err
	}
	resp, err := v.cfg.Client.Do(req) // #nosec G704
	if err != nil {
		return fmt.Errorf("auth: fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: fetch jwks: status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("auth: fetch jwks: %w", err)
	}
	keys, err := parseJWKS(b)
	if err != nil {
		return err
	}
	v.keys, v.fetched = keys, true
	return nil
}

type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	X   string `json:"x"`
	Use string `json:"use"`
}

func parseJWKS(b []byte) (map[string]ed25519.PublicKey, error) {
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("auth: parse jwks: %w", err)
	}
	out := map[string]ed25519.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" || k.Kid == "" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		out[k.Kid] = ed25519.PublicKey(raw)
	}
	if len(out) == 0 {
		return nil, errors.New("auth: jwks has no usable Ed25519 keys")
	}
	return out, nil
}

func parseKeyFile(b []byte) (map[string]ed25519.PublicKey, error) {
	if blk, _ := pem.Decode(b); blk != nil {
		pub, err := x509.ParsePKIXPublicKey(blk.Bytes)
		if err != nil {
			return nil, err
		}
		ek, ok := pub.(ed25519.PublicKey)
		if !ok {
			return nil, errors.New("not an Ed25519 public key")
		}
		return map[string]ed25519.PublicKey{"": ek}, nil
	}
	return parseJWKS(b)
}

// Verify implements Verifier.
func (v *JWTVerifier) Verify(ctx context.Context, token string) (Principal, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > maxHeaderLen {
		return Principal{}, ErrInvalidToken
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &hdr); err != nil || hdr.Alg != "EdDSA" {
		return Principal{}, ErrInvalidToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return Principal{}, ErrInvalidToken
	}
	key, err := v.key(ctx, hdr.Kid)
	if err != nil {
		return Principal{}, err
	}
	if !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return Principal{}, ErrInvalidToken
	}
	var c struct {
		Iss   string          `json:"iss"`
		Sub   string          `json:"sub"`
		Aud   json.RawMessage `json:"aud"`
		Scope string          `json:"scope"`
		Exp   int64           `json:"exp"`
		Nbf   int64           `json:"nbf"`
		JTI   string          `json:"jti"`
		Kind  string          `json:"kind"`
		SID   string          `json:"sid"`
		Par   string          `json:"par"`
	}
	if err := decodePart(parts[1], &c); err != nil || c.Sub == "" || c.Exp == 0 {
		return Principal{}, ErrInvalidToken
	}
	now := v.cfg.Now()
	if !now.Add(-v.cfg.Leeway).Before(time.Unix(c.Exp, 0)) ||
		(c.Nbf != 0 && now.Add(v.cfg.Leeway).Before(time.Unix(c.Nbf, 0))) {
		return Principal{}, ErrInvalidToken
	}
	if v.cfg.Issuer != "" && c.Iss != v.cfg.Issuer {
		return Principal{}, ErrInvalidToken
	}
	if !audienceMatches(c.Aud, v.cfg.Audience) {
		return Principal{}, ErrInvalidToken
	}
	p := Principal{Verified: true, ID: c.Sub, Kind: c.Kind, Scopes: strings.Fields(c.Scope),
		Audience: v.cfg.Audience, SessionID: c.SID, Parent: c.Par, TokenID: c.JTI, ExpiresAt: time.Unix(c.Exp, 0)}
	if v.cfg.DenyList.Denied(p) {
		return Principal{}, ErrInvalidToken
	}
	return p, nil
}

// key resolves kid, refetching the JWKS (rate limited) on a miss so a freshly
// rotated key is picked up without waiting for the periodic refresh.
func (v *JWTVerifier) key(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.lookup(kid); ok {
		return k, nil
	}
	var fetchErr error
	if v.cfg.JWKSURL != "" && (v.lastFetch.IsZero() || v.cfg.Now().Sub(v.lastFetch) >= v.cfg.MinRefresh) {
		fetchErr = v.refreshLocked(ctx)
		if k, ok := v.lookup(kid); ok {
			return k, nil
		}
	}
	if fetchErr != nil || (!v.fetched && len(v.keys) == 0) {
		return nil, fmt.Errorf("%w: no key for kid %q", ErrUnavailable, kid)
	}
	return nil, ErrInvalidToken
}

func (v *JWTVerifier) lookup(kid string) (ed25519.PublicKey, bool) {
	if k, ok := v.keys[kid]; ok {
		return k, true
	}
	k, ok := v.keys[""] // PEM bootstrap key accepts any kid
	return k, ok
}

func decodePart(s string, dst any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

func audienceMatches(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}

func isInvalid(err error) bool { return errors.Is(err, ErrInvalidToken) }
