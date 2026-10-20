package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Dev service tokens are ordinary Tether JWTs with kind=service and a very
// distant exp. The verifier rejects exp==0 (see TestJWTRejects "no exp"), so
// "no expiry" is expressed as a far-future exp and revoked through the
// deny-list. These vectors pin that contract for issuers and consumers.

const serviceLifetime = 10 * 365 * 24 * time.Hour

func serviceClaims(over map[string]any) map[string]any {
	c := claims(map[string]any{
		"sub":   "msg://service/local/basalisk",
		"scope": "tesseract.read tesseract.write",
		"kind":  "service",
		"jti":   "svc-1",
		"exp":   time.Now().Add(serviceLifetime).Unix(),
	})
	delete(c, "sid") // a service token is not bound to an agent session
	for k, v := range over {
		c[k] = v
	}
	return c
}

func TestServiceTokenVerifies(t *testing.T) {
	is := newIssuer(t, "k1")
	p, err := newVerifier(t, is, nil).Verify(context.Background(), is.token("k1", serviceClaims(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Verified || p.Kind != "service" || p.ID != "msg://service/local/basalisk" || p.TokenID != "svc-1" || p.SessionID != "" {
		t.Fatalf("bad principal %+v", p)
	}
	if !p.HasScope("tesseract.write") || p.HasScope("tesseract.admin") {
		t.Fatalf("bad scopes %+v", p.Scopes)
	}
	if got := time.Until(p.ExpiresAt); got < serviceLifetime-time.Hour {
		t.Fatalf("exp not preserved: %v away", got)
	}
}

func TestServiceTokenStillValidYearsLater(t *testing.T) {
	is := newIssuer(t, "k1")
	tok := is.token("k1", serviceClaims(nil))
	later := time.Now().Add(5 * 365 * 24 * time.Hour)
	v := newVerifier(t, is, func(c *JWTConfig) { c.Now = func() time.Time { return later } })
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("service token rejected after 5 years: %v", err)
	}
}

func TestServiceTokenRejects(t *testing.T) {
	is := newIssuer(t, "k1")
	v := newVerifier(t, is, nil)
	cases := map[string]string{
		"no exp (zero)":  is.token("k1", serviceClaims(map[string]any{"exp": 0})),
		"expired":        is.token("k1", serviceClaims(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})),
		"wrong audience": is.token("k1", serviceClaims(map[string]any{"aud": "torque"})),
		"wrong issuer":   is.token("k1", serviceClaims(map[string]any{"iss": "evil"})),
		"no subject":     is.token("k1", serviceClaims(map[string]any{"sub": ""})),
	}
	for name, tok := range cases {
		if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: want ErrInvalidToken, got %v", name, err)
		}
	}
}

// The deny-list is the only revocation path for a token that outlives every
// poll window, so each identifier the issuer can revoke by must work, and an
// entry must keep working for as long as the token would be valid.
func TestServiceTokenRevocation(t *testing.T) {
	is := newIssuer(t, "k1")
	dl := NewDenyList()
	v := newVerifier(t, is, func(c *JWTConfig) { c.DenyList = dl })
	ctx := context.Background()
	tok := is.token("k1", serviceClaims(nil))
	if _, err := v.Verify(ctx, tok); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]DenyListDoc{
		"jti": {JTI: []string{"svc-1"}},
		"sub": {Sub: []string{"msg://service/local/basalisk"}},
	} {
		dl.Replace(doc)
		if _, err := v.Verify(ctx, tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s revoked service token accepted: %v", name, err)
		}
	}
	dl.Replace(DenyListDoc{JTI: []string{"someone-else"}, SID: []string{"s1"}})
	if _, err := v.Verify(ctx, tok); err != nil {
		t.Fatalf("unrelated deny entries rejected the token: %v", err)
	}
	// An agent session being denied must not take down a service token that
	// carries no sid.
	dl.Replace(DenyListDoc{SID: []string{""}})
	if _, err := v.Verify(ctx, tok); err != nil {
		t.Fatalf("empty sid entry rejected a token with no sid: %v", err)
	}
}

func TestServiceTokenThroughGuard(t *testing.T) {
	is := newIssuer(t, "k1")
	dl := NewDenyList()
	v := newVerifier(t, is, func(c *JWTConfig) { c.DenyList = dl })
	scopes := NewScopeTable().
		Route("GET", "/notes/{id}", "tesseract.read").
		Route("*", "/admin/*", "tesseract.admin")
	g, err := New(Config{Mode: ModeEnforce, Verifier: v, Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	h := g.Middleware(okHandler())
	good := "Bearer " + is.token("k1", serviceClaims(nil))
	if got := do(h, "GET", "/notes/7", good).Code; got != 200 {
		t.Fatalf("service token with scope: got %d want 200", got)
	}
	if got := do(h, "GET", "/admin/x", good).Code; got != 403 {
		t.Fatalf("service token without admin scope: got %d want 403", got)
	}
	dl.Replace(DenyListDoc{JTI: []string{"svc-1"}})
	if got := do(h, "GET", "/notes/7", good).Code; got != 401 {
		t.Fatalf("revoked service token: got %d want 401", got)
	}
}

// An app-wide wildcard grant (`<app>.*`) is honored by the verifier; whether
// to issue one is the issuer's policy (dev only, with a warning).
func TestServiceTokenAppWildcardScope(t *testing.T) {
	is := newIssuer(t, "k1")
	p, err := newVerifier(t, is, nil).Verify(context.Background(),
		is.token("k1", serviceClaims(map[string]any{"scope": "tesseract.*"})))
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasScope("tesseract.admin") || p.HasScope("torque.read") {
		t.Fatalf("wildcard scope not app-bounded: %+v", p.Scopes)
	}
}
