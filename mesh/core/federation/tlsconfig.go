package federation

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// DefaultHTTPTimeout bounds one outbound federation request made through
// ClientHTTPClient.
const DefaultHTTPTimeout = 30 * time.Second

// pinCheck returns the tls.Config.VerifyConnection hook shared by both ends. It
// accepts a connection iff the presented leaf is inside its validity window and
// its SHA-256 fingerprint satisfies pinned. role ("client" or "server") only
// shapes the error.
//
// It is VerifyConnection rather than VerifyPeerCertificate on purpose: Go calls
// VerifyConnection on every handshake, resumed sessions included, whereas
// VerifyPeerCertificate is skipped on resumption. A pin that was removed must not
// keep working through a session ticket.
func pinCheck(pinned func(fp string) bool, role string, now func() time.Time) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 || cs.PeerCertificates[0] == nil {
			return fmt.Errorf("federation: no %s certificate presented", role)
		}
		leaf := cs.PeerCertificates[0]
		if err := checkValidity(leaf, now()); err != nil {
			return fmt.Errorf("federation: %s %w", role, err)
		}
		if fp := Fingerprint(leaf); !pinned(fp) {
			return fmt.Errorf("federation: %s certificate %s is not pinned", role, fp)
		}
		return nil
	}
}

// ServerTLSConfig builds the *tls.Config for the federation listener. Peer
// identity is established by pinning the leaf certificate's fingerprint, not by
// a CA chain:
//
//   - ClientAuth is RequireAnyClientCert: a client certificate is mandatory, but
//     Go is told not to chain-verify it (a pinned self-signed certificate has no
//     chain, and RequireAndVerifyClientCert would fail the handshake before the
//     pin is consulted). Pinning is the verification, and it is stricter than a
//     CA: every check happens in VerifyConnection.
//   - VerifyConnection rejects an unpinned, expired or not-yet-valid certificate,
//     on every handshake including resumed ones.
//   - TLS 1.3 is the minimum. Both ends are ours, so there is no reason to
//     accept older.
//
// identity is this install's certificate. It returns an error for an empty
// identity or a nil or empty registry, so a listener can never start unpinned.
func ServerTLSConfig(identity tls.Certificate, peers *PeerRegistry) (*tls.Config, error) {
	if len(identity.Certificate) == 0 || identity.PrivateKey == nil {
		return nil, errors.New("federation: server TLS config needs an identity certificate and key")
	}
	if peers == nil || peers.PinCount() == 0 {
		return nil, errors.New("federation: server TLS config needs at least one pinned peer")
	}
	return &tls.Config{
		Certificates:     []tls.Certificate{identity},
		MinVersion:       tls.VersionTLS13,
		ClientAuth:       tls.RequireAnyClientCert,
		VerifyConnection: pinCheck(func(fp string) bool { _, ok := peers.Lookup(fp); return ok }, "client", time.Now),
	}, nil
}

// ClientTLSConfig builds the *tls.Config an outbound federation client dials a
// peer with. It presents identity as the client certificate and pins the peer's
// server certificate by SHA-256 fingerprint. serverPins must be non-empty: a
// route can never quietly become unauthenticated.
//
// InsecureSkipVerify turns off Go's chain and hostname verification because the
// peer's certificate is self-signed and has neither. It is not a downgrade:
// VerifyConnection replaces that check with an exact-leaf pin plus expiry, which
// is stricter than a chain.
func ClientTLSConfig(identity tls.Certificate, serverPins []string) (*tls.Config, error) {
	if len(identity.Certificate) == 0 || identity.PrivateKey == nil {
		return nil, errors.New("federation: client TLS config needs an identity certificate and key")
	}
	pins := make(map[string]struct{}, len(serverPins))
	for _, p := range serverPins {
		norm, err := ValidateFingerprint(p)
		if err != nil {
			return nil, err
		}
		pins[norm] = struct{}{}
	}
	if len(pins) == 0 {
		return nil, errors.New("federation: client TLS config requires at least one server pin")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{identity},
		MinVersion:   tls.VersionTLS13,
		//nolint:gosec // G402: the default verifier is replaced by VerifyConnection, which pins the exact leaf and checks its validity window.
		InsecureSkipVerify: true,
		VerifyConnection:   pinCheck(func(fp string) bool { _, ok := pins[fp]; return ok }, "server", time.Now),
	}, nil
}

// ClientHTTPClient builds the mutual-TLS *http.Client used to reach one peer:
// this install's certificate presented, the peer's pinned. It is what Dial hands
// to httpstore's WithHTTPClient seam.
func ClientHTTPClient(identity tls.Certificate, serverPins []string) (*http.Client, error) {
	tc, err := ClientTLSConfig(identity, serverPins)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: DefaultHTTPTimeout,
		Transport: &http.Transport{
			TLSClientConfig:     tc,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConns:        16,
			IdleConnTimeout:     90 * time.Second,
			// A federation hop never follows a redirect to another host: the pin
			// is for this endpoint only.
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}
