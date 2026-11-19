package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyKeepsUnmappedClaims(t *testing.T) {
	is := newIssuer(t, "k1")
	v, err := NewJWTVerifier(JWTConfig{Issuer: "https://tether", Audience: aud, JWKSURL: is.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	tok := is.token("k1", claims(map[string]any{"grant": "grt_1", "act": map[string]any{"sub": "agent:lead", "kind": "agent"}}))
	p, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Extra["grant"]) != `"grt_1"` {
		t.Fatalf("grant extra %s", p.Extra["grant"])
	}
	var act struct{ Sub string }
	if err = json.Unmarshal(p.Extra["act"], &act); err != nil || act.Sub != "agent:lead" {
		t.Fatalf("act extra %s: %v", p.Extra["act"], err)
	}
	for _, mapped := range []string{"iss", "sub", "aud", "scope", "exp", "iat", "jti", "kind", "sid"} {
		if _, ok := p.Extra[mapped]; ok {
			t.Errorf("mapped claim %s repeated in Extra", mapped)
		}
	}
	// A token with only mapped claims has no Extra.
	p2, err := v.Verify(context.Background(), is.token("k1", claims(nil)))
	if err != nil || p2.Extra != nil {
		t.Fatalf("plain token Extra %v err %v", p2.Extra, err)
	}
}

func rotatingIssuer(t *testing.T, body map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPMinterOnRotate(t *testing.T) {
	srv := rotatingIssuer(t, map[string]any{"token": "jwt-2", "ttl_seconds": 900, "refresh_token": "wdn_p_next"})
	var stored string
	m := &HTTPMinter{URL: srv.URL, Body: map[string]any{"grant_type": "refresh_token"},
		Credential: func() (string, error) { return "wdn_p_first", nil },
		OnRotate:   func(next string) error { stored = next; return nil }}
	got, err := m.Mint(context.Background())
	if err != nil || got.Token != "jwt-2" || stored != "wdn_p_next" {
		t.Fatalf("mint %+v err %v stored %q", got, err, stored)
	}
	// A failed store fails the mint (the caller must not use a token whose parent was lost).
	m.OnRotate = func(string) error { return errors.New("disk full") }
	if _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("mint succeeded although the rotated parent was not stored")
	}
	// An issuer that rotates, with no hook set: fail loudly instead of dropping the only credential.
	m.OnRotate = nil
	if _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("mint succeeded and dropped the rotated parent")
	}
}

func TestHTTPMinterNoRotationUnchanged(t *testing.T) {
	srv := rotatingIssuer(t, map[string]any{"token": "jwt-1", "ttl_seconds": 900})
	called := false
	m := &HTTPMinter{URL: srv.URL, Credential: func() (string, error) { return "parent", nil },
		OnRotate: func(string) error { called = true; return nil }}
	if _, err := m.Mint(context.Background()); err != nil || called {
		t.Fatalf("err %v, OnRotate called %v", err, called)
	}
}
