package federation

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"sort"
	"time"
)

// Errors an IdentityResolver returns.
var (
	// ErrUnauthenticated: the request carries no usable credential (no TLS, no
	// client certificate, a certificate outside its validity window). A server
	// answers 401.
	ErrUnauthenticated = errors.New("federation: caller is not authenticated")
	// ErrUnknownPeer: a credential was presented but it is not one this install
	// has registered. A server answers 403.
	ErrUnknownPeer = errors.New("federation: caller is not a registered peer")
)

// Identity is who an inbound caller is, as far as this install has established:
// a label for the audit log and the authorities the caller may act for.
type Identity struct {
	// Label is the operator-facing name of the peer.
	Label string
	// Authorities are the URN authorities the caller may originate mail for and
	// take part in threads of. An identity with none may do nothing.
	Authorities []string
	// Fingerprint identifies the credential that established the identity (for
	// mTLS, the leaf certificate's SHA-256), for the audit log.
	Fingerprint string
}

// IsAuthoritative reports whether the caller may act for authority. The empty
// authority is never authoritative.
func (i Identity) IsAuthoritative(authority string) bool {
	if authority == "" {
		return false
	}
	for _, a := range i.Authorities {
		if a == authority {
			return true
		}
	}
	return false
}

// IdentityResolver authenticates an inbound federation request and says which
// authorities the caller may act for. It is the seam a trust model plugs into.
//
// Resolve must fail closed: an error, whatever it is, refuses the request. It
// should wrap ErrUnauthenticated when there is no credential at all and
// ErrUnknownPeer when there is one this install does not recognize; anything
// else is treated as a refusal for an unknown reason.
//
// The only resolver this module ships is MTLSPinnedResolver. It deliberately
// ships no resolver that trusts an identity the caller merely asserts (Tether's
// ?as= parameter): if one is ever added it must be named unmistakably as insecure
// and be a later, explicit opt-in.
type IdentityResolver interface {
	Resolve(ctx context.Context, r *http.Request) (Identity, error)
}

// MTLSPinnedResolver resolves a caller from its verified client certificate: the
// SHA-256 fingerprint of the leaf, looked up in peers. It refuses a request that
// did not arrive over TLS, one with no client certificate, a certificate outside
// its validity window, and a fingerprint that is not pinned. It does not depend
// on the TLS configuration having checked any of that already: a pin that was
// removed, or an expired certificate, is refused here whatever the handshake did.
func MTLSPinnedResolver(peers *PeerRegistry) IdentityResolver {
	return &mtlsResolver{peers: peers, now: time.Now}
}

type mtlsResolver struct {
	peers *PeerRegistry
	now   func() time.Time
}

func (m *mtlsResolver) Resolve(_ context.Context, r *http.Request) (Identity, error) {
	if r == nil || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return Identity{}, ErrUnauthenticated
	}
	leaf := r.TLS.PeerCertificates[0]
	if leaf == nil {
		return Identity{}, ErrUnauthenticated
	}
	if err := checkValidity(leaf, m.now()); err != nil {
		return Identity{}, errors.Join(ErrUnauthenticated, err)
	}
	fp := Fingerprint(leaf)
	peer, ok := m.peers.Lookup(fp)
	if !ok {
		return Identity{}, ErrUnknownPeer
	}
	return Identity{Label: peer.Label, Authorities: peer.Authorities(), Fingerprint: fp}, nil
}

// checkValidity refuses a certificate outside its NotBefore/NotAfter window.
func checkValidity(cert *x509.Certificate, now time.Time) error {
	if now.Before(cert.NotBefore) {
		return errors.New("certificate is not yet valid")
	}
	if now.After(cert.NotAfter) {
		return errors.New("certificate has expired")
	}
	return nil
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
