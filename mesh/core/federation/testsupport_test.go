package federation

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gomsg "github.com/hollis-labs/go-messaging"
	"github.com/hollis-labs/go-messaging/memstore"
)

func newIdentity(t testing.TB, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "federation-test"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func validIdentity(t testing.TB) tls.Certificate {
	now := time.Now()
	return newIdentity(t, now.Add(-time.Hour), now.Add(365*24*time.Hour))
}

func expiredIdentity(t testing.TB) tls.Certificate {
	now := time.Now()
	return newIdentity(t, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
}

func futureIdentity(t testing.TB) tls.Certificate {
	now := time.Now()
	return newIdentity(t, now.Add(24*time.Hour), now.Add(48*time.Hour))
}

func fp(c tls.Certificate) string { return Fingerprint(c.Leaf) }

func writeIdentityPEM(t testing.TB, dir, name string, id tls.Certificate) {
	t.Helper()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: id.Certificate[0]})
	keyDER, err := x509.MarshalPKCS8PrivateKey(id.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, name+".crt"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".key"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

func addr(kind gomsg.AddressKind, authority, id string) gomsg.Address {
	return gomsg.Address{Kind: kind, Authority: authority, ID: id}
}

func agent(authority, id string) gomsg.Address { return addr(gomsg.KindAgent, authority, id) }

func notice(from, to gomsg.Address) gomsg.Envelope {
	return gomsg.Envelope{Kind: gomsg.MsgKindNotice, From: from, To: to}
}

// spyStore counts what reaches the store and can be told to fail or panic.
type spyStore struct {
	gomsg.Store
	mu    sync.Mutex
	calls map[string]int
	err   error
	panic bool
}

func newSpy() *spyStore { return &spyStore{Store: memstore.New(), calls: map[string]int{}} }

func (s *spyStore) hit(op string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[op]++
	if s.panic {
		panic("spy store panic: secret-internal-detail")
	}
	return s.err
}

func (s *spyStore) count(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[op]
}

func (s *spyStore) Send(ctx context.Context, e gomsg.Envelope) (gomsg.Envelope, error) {
	if err := s.hit("send"); err != nil {
		return gomsg.Envelope{}, err
	}
	return s.Store.Send(ctx, e)
}
func (s *spyStore) Get(ctx context.Context, id string) (gomsg.Envelope, error) {
	if err := s.hit("get"); err != nil {
		return gomsg.Envelope{}, err
	}
	return s.Store.Get(ctx, id)
}
func (s *spyStore) Inbox(ctx context.Context, to gomsg.Address, f gomsg.Filter) ([]gomsg.Envelope, error) {
	if err := s.hit("inbox"); err != nil {
		return nil, err
	}
	return s.Store.Inbox(ctx, to, f)
}
func (s *spyStore) Thread(ctx context.Context, id string, f gomsg.Filter) ([]gomsg.Envelope, error) {
	if err := s.hit("thread"); err != nil {
		return nil, err
	}
	return s.Store.Thread(ctx, id, f)
}
func (s *spyStore) Consume(ctx context.Context, id string, r gomsg.Address) error {
	if err := s.hit("consume"); err != nil {
		return err
	}
	return s.Store.Consume(ctx, id, r)
}
func (s *spyStore) Cancel(ctx context.Context, id string) error {
	if err := s.hit("cancel"); err != nil {
		return err
	}
	return s.Store.Cancel(ctx, id)
}
func (s *spyStore) Subscribe(ctx context.Context, to gomsg.Address, f gomsg.Filter) (<-chan gomsg.Envelope, error) {
	if err := s.hit("subscribe"); err != nil {
		return nil, err
	}
	return s.Store.Subscribe(ctx, to, f)
}

// hop is a running federation server plus the material to dial it.
type hop struct {
	t        testing.TB
	store    *spyStore
	server   *Server
	url      string
	identity tls.Certificate // the server's
	audit    *auditLog
}

type auditLog struct {
	mu   sync.Mutex
	recs []AuditRecord
}

func (a *auditLog) add(r AuditRecord) { a.mu.Lock(); a.recs = append(a.recs, r); a.mu.Unlock() }
func (a *auditLog) all() []AuditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AuditRecord(nil), a.recs...)
}
func (a *auditLog) last() AuditRecord {
	all := a.all()
	if len(all) == 0 {
		return AuditRecord{}
	}
	return all[len(all)-1]
}

// startHop runs a server homing localAuthorities that admits peers, over real mutual TLS.
func startHop(t testing.TB, ops OpSet, local []string, peers []PeerConfig, extra ...ServerOption) *hop {
	t.Helper()
	reg, err := NewPeerRegistry(peers)
	if err != nil {
		t.Fatal(err)
	}
	h := &hop{t: t, store: newSpy(), identity: validIdentity(t), audit: &auditLog{}}
	opts := append([]ServerOption{WithAuditor(h.audit.add)}, extra...)
	h.server, err = NewServer(h.store, MTLSPinnedResolver(reg), ops, local, opts...)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := ServerTLSConfig(h.identity, reg)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.server.Serve(ctx, ln, tc) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	h.url = "https://" + ln.Addr().String()
	return h
}

// dial returns a Store that reaches the hop as client, pinning the hop's certificate.
func (h *hop) dial(client tls.Certificate, opts ...DialOption) gomsg.Store {
	h.t.Helper()
	all := append([]DialOption{WithIdentity(client), WithServerPins(fp(h.identity))}, opts...)
	s, err := Dial(h.url, all...)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func peer(label string, cert tls.Certificate, authorities ...string) PeerConfig {
	return PeerConfig{Label: label, Fingerprints: []string{fp(cert)}, Authorities: authorities}
}

func listenLoopback(t testing.TB) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}
