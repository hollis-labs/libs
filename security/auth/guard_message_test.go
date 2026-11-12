package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

type failingVerifier struct{}

func (failingVerifier) Verify(context.Context, string) (Principal, error) {
	return Principal{}, errors.New("backend down")
}

// Each rejection code carries a message that matches its cause; the code and
// status are part of the contract and are asserted alongside.
func TestRejectionMessageMatchesCode(t *testing.T) {
	scopes := NewScopeTable().Route("GET", "/notes", "tesseract.read")
	newH := func(v Verifier) http.Handler {
		g, err := New(Config{Mode: ModeEnforce, Verifier: v, Scopes: scopes})
		if err != nil {
			t.Fatal(err)
		}
		return g.Middleware(okHandler())
	}
	good := NewAPIKeyVerifier(APIKey{Hash: HashKey("k"), ID: "svc/k", Scopes: []string{"tesseract.read"}})
	narrow := NewAPIKeyVerifier(APIKey{Hash: HashKey("n"), ID: "svc/n", Scopes: []string{"other"}})
	cases := []struct {
		name, path, authz string
		h                 http.Handler
		status            int
		code, msg         string
	}{
		{"unauthorized", "/notes", "", newH(good), 401, "unauthorized", "verified caller identity required"},
		{"route_not_mapped", "/nowhere", "Bearer k", newH(good), 403, "route_not_mapped", "no scope is mapped for this route"},
		{"scope_required", "/notes", "Bearer n", newH(narrow), 403, "scope_required", "required scope: tesseract.read"},
		{"auth_unavailable", "/notes", "Bearer k", newH(failingVerifier{}), 503, "auth_unavailable", "authentication is temporarily unavailable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(c.h, "GET", c.path, c.authz)
			var body struct {
				Error struct{ Code, Message string } `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rec.Code != c.status || body.Error.Code != c.code || body.Error.Message != c.msg {
				t.Fatalf("got %d %q %q; want %d %q %q", rec.Code, body.Error.Code, body.Error.Message, c.status, c.code, c.msg)
			}
		})
	}
}
