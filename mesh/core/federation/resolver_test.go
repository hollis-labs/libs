package federation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"testing"
	"time"
)

func reqWith(certs ...*x509.Certificate) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "https://x/", nil)
	r.TLS = &tls.ConnectionState{PeerCertificates: certs}
	return r
}

// The contract of MTLSPinnedResolver: it authenticates from the certificate
// alone, refuses everything else, and reports the peer's authorities. It does not
// rely on the TLS config having checked anything.
func TestMTLSPinnedResolverContract(t *testing.T) {
	pinned, expired, future, stranger := validIdentity(t), expiredIdentity(t), futureIdentity(t), validIdentity(t)
	reg, err := NewPeerRegistry([]PeerConfig{
		peer("pinned", pinned, "b", "a"),
		peer("expired", expired, "e"), // pinned, but its certificate has expired
		peer("future", future, "f"),
	})
	if err != nil {
		t.Fatal(err)
	}
	res := MTLSPinnedResolver(reg)

	id, err := res.Resolve(context.Background(), reqWith(pinned.Leaf))
	if err != nil {
		t.Fatal(err)
	}
	if id.Label != "pinned" || id.Fingerprint != fp(pinned) || len(id.Authorities) != 2 || id.Authorities[0] != "a" || id.Authorities[1] != "b" {
		t.Fatalf("Identity = %+v", id)
	}
	if !id.IsAuthoritative("a") || id.IsAuthoritative("e") || id.IsAuthoritative("") {
		t.Error("the identity's authorities are the pinned peer's, sorted")
	}

	plain, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	refusals := map[string]struct {
		req  *http.Request
		want error
	}{
		"plain HTTP":                   {plain, ErrUnauthenticated},
		"nil request":                  {nil, ErrUnauthenticated},
		"TLS with no certificate":      {reqWith(), ErrUnauthenticated},
		"nil certificate":              {reqWith(nil), ErrUnauthenticated},
		"expired, though pinned":       {reqWith(expired.Leaf), ErrUnauthenticated},
		"not yet valid, though pinned": {reqWith(future.Leaf), ErrUnauthenticated},
		"unpinned":                     {reqWith(stranger.Leaf), ErrUnknownPeer},
		"only the leaf counts":         {reqWith(stranger.Leaf, pinned.Leaf), ErrUnknownPeer},
	}
	for name, tc := range refusals {
		got, err := res.Resolve(context.Background(), tc.req)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
		if got.Label != "" || len(got.Authorities) != 0 {
			t.Errorf("%s: a refusal must return no identity, got %+v", name, got)
		}
	}
}

func TestResolverReflectsARemovedPin(t *testing.T) {
	a, b := validIdentity(t), validIdentity(t)
	before, _ := NewPeerRegistry([]PeerConfig{peer("a", a, "x"), peer("b", b, "y")})
	after, _ := NewPeerRegistry([]PeerConfig{peer("a", a, "x")})
	if _, err := MTLSPinnedResolver(before).Resolve(context.Background(), reqWith(b.Leaf)); err != nil {
		t.Fatal(err)
	}
	if _, err := MTLSPinnedResolver(after).Resolve(context.Background(), reqWith(b.Leaf)); !errors.Is(err, ErrUnknownPeer) {
		t.Fatalf("a removed pin must stop resolving: %v", err)
	}
}

func TestResolverWithANilRegistryRefusesEveryone(t *testing.T) {
	if _, err := MTLSPinnedResolver(nil).Resolve(context.Background(), reqWith(validIdentity(t).Leaf)); !errors.Is(err, ErrUnknownPeer) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckValidityBoundaries(t *testing.T) {
	c := validIdentity(t).Leaf
	if checkValidity(c, c.NotBefore.Add(-time.Second)) == nil || checkValidity(c, c.NotAfter.Add(time.Second)) == nil {
		t.Error("outside the window must be refused")
	}
	if checkValidity(c, c.NotBefore.Add(time.Second)) != nil || checkValidity(c, c.NotAfter.Add(-time.Second)) != nil {
		t.Error("inside the window must be accepted")
	}
}
