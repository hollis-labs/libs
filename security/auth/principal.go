// Package auth is the shared bearer-auth contract for the Hollis mesh: a
// Principal in request context, a Verifier interface, JWT/JWKS and static
// API-key verifiers, the off|permissive|enforce modes, and a route/tool to
// scope table.
package auth

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Errors returned by Verifiers and Guard.AuthorizeTool.
var (
	// ErrInvalidToken means the credential is malformed, unknown, expired,
	// for another audience, or revoked.
	ErrInvalidToken = errors.New("auth: invalid or inactive token")
	// ErrUnavailable means the verifier could not decide (for example the
	// JWKS endpoint is unreachable and no key is cached).
	ErrUnavailable = errors.New("auth: verifier unavailable")
	// ErrUnauthenticated is returned by AuthorizeTool without a verified principal.
	ErrUnauthenticated = errors.New("auth: verified caller identity required")
	// ErrForbidden is returned by AuthorizeTool when a scope is missing.
	ErrForbidden = errors.New("auth: required scope not granted")
)

// Principal kinds carried in the "kind" claim.
const (
	KindOperator = "operator"
	KindService  = "service"
	KindDevice   = "device"
	KindAgent    = "agent"
)

// OffModePrincipalID is the synthetic principal stamped in off mode when no
// valid token is presented.
const OffModePrincipalID = "operator(off-mode)"

// Principal is the authenticated caller. Verified is true only when a bearer
// credential resolved to it; the off-mode synthetic principal is not verified.
type Principal struct {
	Verified  bool      `json:"verified"`
	ID        string    `json:"id"`
	Kind      string    `json:"kind,omitempty"`
	Display   string    `json:"display,omitempty"`
	Scopes    []string  `json:"scopes,omitempty"`
	Audience  string    `json:"audience,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Parent    string    `json:"parent,omitempty"`
	TokenID   string    `json:"token_id,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// HasScope reports whether the principal holds scope. A grant matches when it
// equals scope, is "*", or is an "<app>.*" wildcard covering scope.
func (p Principal) HasScope(scope string) bool {
	for _, g := range p.Scopes {
		if g == scope || g == "*" {
			return true
		}
		if pre, ok := strings.CutSuffix(g, ".*"); ok && strings.HasPrefix(scope, pre+".") {
			return true
		}
	}
	return false
}

type principalKey struct{}

// WithPrincipal stores a copy of p in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	p.Scopes = append([]string(nil), p.Scopes...)
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the principal stamped by the middleware, if any.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	p.Scopes = append([]string(nil), p.Scopes...)
	return p, ok
}

// Verified reports the one verified flag for ctx.
func Verified(ctx context.Context) bool {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return ok && p.Verified
}

// Verifier resolves a bearer token (without the "Bearer " prefix) to a
// Principal. It returns ErrInvalidToken for a bad credential and ErrUnavailable
// (or any other error) when it cannot decide.
type Verifier interface {
	Verify(ctx context.Context, token string) (Principal, error)
}

// Authentication states reported in audit events.
const (
	StateMissing     = "missing"
	StateInvalid     = "invalid"
	StateUnavailable = "unavailable"
	StateVerified    = "verified"
	StateOffMode     = "off-mode"
)

const maxHeaderLen = 4096

// ParseBearer extracts the token from Authorization header values. It accepts
// exactly one header of the form "Bearer <token>" and reports StateMissing,
// StateInvalid, or "" (ok) as the state.
func ParseBearer(headers []string) (token, state string) {
	if len(headers) == 0 {
		return "", StateMissing
	}
	if len(headers) != 1 || len(headers[0]) > maxHeaderLen {
		return "", StateInvalid
	}
	parts := strings.Fields(headers[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", StateInvalid
	}
	return parts[1], ""
}
