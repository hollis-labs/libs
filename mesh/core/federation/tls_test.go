package federation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func stateFor(certs ...*x509.Certificate) tls.ConnectionState {
	return tls.ConnectionState{PeerCertificates: certs}
}

func TestPinCheckDecisions(t *testing.T) {
	good, expired, future := validIdentity(t), expiredIdentity(t), futureIdentity(t)
	other := validIdentity(t)
	pinned := func(f string) bool { return f == fp(good) || f == fp(expired) || f == fp(future) }
	check := pinCheck(pinned, "client", time.Now)
	tests := map[string]struct {
		cs   tls.ConnectionState
		want string // "" = accepted
	}{
		"pinned and valid":     {stateFor(good.Leaf), ""},
		"unpinned":             {stateFor(other.Leaf), "not pinned"},
		"expired":              {stateFor(expired.Leaf), "expired"},
		"not yet valid":        {stateFor(future.Leaf), "not yet valid"},
		"no certificate":       {stateFor(), "no client certificate"},
		"nil certificate":      {stateFor(nil), "no client certificate"},
		"only the leaf counts": {stateFor(good.Leaf, other.Leaf), ""},
		"a pinned second cert does not rescue an unpinned leaf": {stateFor(other.Leaf, good.Leaf), "not pinned"},
	}
	for name, tc := range tests {
		err := check(tc.cs)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// The pin is checked with VerifyConnection, which Go runs on every handshake
// including resumed ones, and never with VerifyPeerCertificate, which it skips on
// resumption: a removed pin must not keep working through a session ticket.
func TestTLSConfigsUseVerifyConnectionOnly(t *testing.T) {
	id := validIdentity(t)
	reg, _ := NewPeerRegistry([]PeerConfig{peer("p", id, "a")})
	sc, err := ServerTLSConfig(id, reg)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := ClientTLSConfig(id, []string{fp(id)})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*tls.Config{"server": sc, "client": cc} {
		if c.VerifyConnection == nil || c.VerifyPeerCertificate != nil {
			t.Errorf("%s: the pin must be VerifyConnection alone", name)
		}
		if c.MinVersion != tls.VersionTLS13 {
			t.Errorf("%s: MinVersion = %x, want TLS 1.3", name, c.MinVersion)
		}
	}
	if sc.ClientAuth != tls.RequireAnyClientCert {
		t.Errorf("server ClientAuth = %v", sc.ClientAuth)
	}
	if !cc.InsecureSkipVerify {
		t.Error("the client replaces chain verification with the pin")
	}
}

func TestTLSConfigBuildersRefuseAnUnpinnedSetup(t *testing.T) {
	id := validIdentity(t)
	reg, _ := NewPeerRegistry([]PeerConfig{peer("p", id, "a")})
	empty, _ := NewPeerRegistry(nil)
	if _, err := ServerTLSConfig(tls.Certificate{}, reg); err == nil {
		t.Error("no identity")
	}
	if _, err := ServerTLSConfig(id, nil); err == nil {
		t.Error("nil registry")
	}
	if _, err := ServerTLSConfig(id, empty); err == nil {
		t.Error("a listener with no pinned peer must not start")
	}
	if _, err := ClientTLSConfig(tls.Certificate{}, []string{fp(id)}); err == nil {
		t.Error("no client identity")
	}
	if _, err := ClientTLSConfig(id, nil); err == nil {
		t.Error("a client route with no server pin must be refused")
	}
	if _, err := ClientTLSConfig(id, []string{"nothex"}); err == nil {
		t.Error("a malformed pin must be refused")
	}
	c, err := ClientHTTPClient(id, []string{fp(id)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Timeout != DefaultHTTPTimeout {
		t.Errorf("Timeout = %v", c.Timeout)
	}
	if c.CheckRedirect == nil {
		t.Error("a federation client must not follow redirects off the pinned endpoint")
	}
}

// handshake runs a real TLS handshake between a server built from serverID and
// peers, and a client presenting clientID that pins serverPin.
func handshake(t *testing.T, serverID tls.Certificate, peers []PeerConfig, clientID tls.Certificate, serverPin string) error {
	t.Helper()
	reg, err := NewPeerRegistry(peers)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := ServerTLSConfig(serverID, reg)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := ClientTLSConfig(clientID, []string{serverPin})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", sc)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srvErr := make(chan error, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			srvErr <- aerr
			return
		}
		defer c.Close()
		srvErr <- c.(*tls.Conn).Handshake()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := (&tls.Dialer{NetDialer: &net.Dialer{}, Config: cc}).DialContext(ctx, "tcp", ln.Addr().String())
	if err == nil {
		// TLS 1.3 completes the client's handshake before the server has verified
		// the client certificate: the rejection reaches the client on first read.
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err = conn.Read(make([]byte, 1))
		_ = conn.Close()
		if errors.Is(err, io.EOF) || (err != nil && strings.Contains(err.Error(), "i/o timeout")) {
			err = nil // the server accepted the handshake and simply has nothing to say
		}
	}
	serr := <-srvErr
	if err == nil {
		err = serr
	}
	return err
}

func TestRealHandshakes(t *testing.T) {
	server, client := validIdentity(t), validIdentity(t)
	peers := []PeerConfig{peer("c", client, "a")}

	if err := handshake(t, server, peers, client, fp(server)); err != nil {
		t.Errorf("pinned both ways: %v", err)
	}
	if err := handshake(t, server, peers, validIdentity(t), fp(server)); err == nil {
		t.Error("a client certificate that is not pinned must be refused")
	}
	if err := handshake(t, server, peers, expiredIdentity(t), fp(server)); err == nil {
		t.Error("an expired client certificate must be refused")
	}
	expiredPeer := expiredIdentity(t)
	if err := handshake(t, server, []PeerConfig{peer("c", expiredPeer, "a")}, expiredPeer, fp(server)); err == nil {
		t.Error("an expired certificate is refused even when it is pinned")
	}
	if err := handshake(t, server, peers, client, fp(validIdentity(t))); err == nil {
		t.Error("a server certificate that does not match the client's pin must be refused")
	}
	if err := handshake(t, expiredIdentity(t), peers, client, "00"+strings.Repeat("a", 62)); err == nil {
		t.Error("an unpinned, expired server must be refused")
	}
}

// A client with no certificate at all cannot even complete a handshake.
func TestNoClientCertificateIsRefused(t *testing.T) {
	server, client := validIdentity(t), validIdentity(t)
	reg, _ := NewPeerRegistry([]PeerConfig{peer("c", client, "a")})
	sc, _ := ServerTLSConfig(server, reg)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", sc)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.(*tls.Conn).Handshake()
			_ = c.Close()
		}
	}()
	plain := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}}, Timeout: 5 * time.Second} //nolint:gosec // a test client without a certificate
	if resp, err := plain.Get("https://" + ln.Addr().String()); err == nil {
		resp.Body.Close()
		t.Fatal("a client presenting no certificate got an answer")
	}
}
