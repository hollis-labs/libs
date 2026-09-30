package federation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	gomsg "github.com/hollis-labs/go-messaging"
	"github.com/hollis-labs/go-messaging/httpstore"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

type install struct {
	f     *Federation
	store *spyStore
	addr  string
	cert  string // fingerprint
}

// startInstalls brings up one federation per entry of localAuthorities: each homes
// its authorities, pins the others' certificates and routes every other install's
// authorities to it. It goes through the real config path.
func startInstalls(t *testing.T, ops string, localAuthorities ...[]string) []*install {
	t.Helper()
	n := len(localAuthorities)
	ids := make([]struct {
		dir  string
		addr string
	}, n)
	certs := make([]string, n)
	for i := range ids {
		ids[i].dir = t.TempDir()
		ids[i].addr = freeAddr(t)
		id := validIdentity(t)
		writeIdentityPEM(t, ids[i].dir, "self", id)
		certs[i] = fp(id)
	}
	var out []*install
	for i := 0; i < n; i++ {
		peers, routes := "", ""
		for j := 0; j < n; j++ {
			if j == i {
				continue
			}
			auths := ""
			for k, a := range localAuthorities[j] {
				if k > 0 {
					auths += ","
				}
				auths += fmt.Sprintf("%q", a)
				if routes != "" {
					routes += ","
				}
				routes += fmt.Sprintf(`{"authority": %q, "endpoint": "https://%s", "server_pins": ["%s"]}`, a, ids[j].addr, certs[j])
			}
			if peers != "" {
				peers += ","
			}
			peers += fmt.Sprintf(`{"label": "install-%d", "fingerprints": ["%s"], "authorities": [%s]}`, j, certs[j], auths)
		}
		locals := ""
		for k, a := range localAuthorities[i] {
			if k > 0 {
				locals += ","
			}
			locals += fmt.Sprintf("%q", a)
		}
		body := fmt.Sprintf(`{"listen_addr": %q, "local_authorities": [%s], "identity": {"cert_file": "self.crt", "key_file": "self.key"}, %s "peers": [%s], "foreign_routes": [%s]}`,
			ids[i].addr, locals, ops, peers, routes)
		path := filepath.Join(ids[i].dir, "federation.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		store := newSpy()
		f, err := Enable(path, store)
		if err != nil || f == nil {
			t.Fatalf("Enable install %d: %v", i, err)
		}
		out = append(out, &install{f: f, store: store, addr: ids[i].addr, cert: certs[i]})
	}
	for i, in := range out {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- in.f.Run(ctx) }()
		t.Cleanup(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("install %d: Run returned %v", i, err)
			}
		})
	}
	for _, in := range out { // wait for the listeners
		deadline := time.Now().Add(10 * time.Second)
		for {
			c, err := net.Dial("tcp", in.addr)
			if err == nil {
				_ = c.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("listener %s never came up", in.addr)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return out
}

// Two installs, each homing an authority: a message sent through one's Store to the
// other's authority is delivered over mutual TLS into the other's local Store, and
// a reply goes back the same way.
func TestTwoInstallsExchangeMessagesThroughTheRouter(t *testing.T) {
	ins := startInstalls(t, "", []string{"alpha"}, []string{"beta"})
	a, b := ins[0], ins[1]
	ctx := context.Background()

	req, err := a.f.Store().Send(ctx, notice(agent("alpha", "x"), agent("beta", "y")))
	if err != nil {
		t.Fatalf("alpha -> beta: %v", err)
	}
	got, err := b.store.Store.Get(ctx, req.ID) // the local store of beta
	if err != nil || got.To != agent("beta", "y") {
		t.Fatalf("beta's store: %+v, %v", got, err)
	}
	if a.store.count("send") != 0 {
		t.Error("a message for a foreign authority must not be stored locally")
	}

	if _, berr := b.f.Store().Send(ctx, gomsg.Envelope{Kind: gomsg.MsgKindNotice, From: agent("beta", "y"), To: agent("alpha", "x")}); berr != nil {
		t.Fatalf("beta -> alpha: %v", berr)
	}
	inbox, err := a.store.Store.Inbox(ctx, agent("alpha", "x"), gomsg.Filter{})
	if err != nil || len(inbox) != 1 {
		t.Fatalf("alpha's inbox: %+v, %v", inbox, err)
	}
	// a local send stays local
	if _, err := a.f.Store().Send(ctx, notice(agent("alpha", "x"), agent("alpha", "z"))); err != nil {
		t.Fatal(err)
	}
	if b.store.count("send") != 1 {
		t.Errorf("beta stored %d messages, want only the one routed to it", b.store.count("send"))
	}
}

// The relay the design forbids: alpha sends to beta a message From an authority
// alpha is not registered for, or to one beta does not home.
func TestTheHopEnforcesTheTrustBoundaryBetweenInstalls(t *testing.T) {
	ins := startInstalls(t, "", []string{"alpha", "alpha2"}, []string{"beta"})
	a := ins[0]
	ctx := context.Background()
	// alpha2 is alpha's, and alpha's install is registered for it at beta: allowed
	if _, err := a.f.Store().Send(ctx, notice(agent("alpha2", "x"), agent("beta", "y"))); err != nil {
		t.Fatalf("alpha2 -> beta: %v", err)
	}
	// forging beta's own authority as the sender is refused by beta's server
	if _, err := a.f.Store().Send(ctx, notice(agent("beta", "forged"), agent("beta", "y"))); err == nil {
		t.Fatal("a peer must not originate mail from an authority it is not registered for")
	}
	if ins[1].store.count("send") != 1 {
		t.Errorf("beta stored %d messages, want 1", ins[1].store.count("send"))
	}
}

// Strictness does not depend on how many authorities an install homes: with one
// the Router's own strict mode rejects an unknown authority, with several a guard
// does, and both give go-messaging's ErrNoRoute.
func TestUnknownAuthoritiesAreRefusedWhetherOneOrManyAreHomed(t *testing.T) {
	for name, locals := range map[string][]string{"one": {"alpha"}, "several": {"alpha", "alpha2"}} {
		t.Run(name, func(t *testing.T) {
			ins := startInstalls(t, "", locals, []string{"beta"})
			s := ins[0].f.Store()
			ctx := context.Background()
			if _, err := s.Send(ctx, notice(agent("alpha", "x"), agent("nowhere", "y"))); !errors.Is(err, gomsg.ErrNoRoute) {
				t.Errorf("Send to an unknown authority: %v", err)
			}
			if _, err := s.Inbox(ctx, agent("nowhere", "y"), gomsg.Filter{}); !errors.Is(err, gomsg.ErrNoRoute) {
				t.Errorf("Inbox for an unknown authority: %v", err)
			}
			if err := s.Consume(ctx, "id", agent("nowhere", "y")); !errors.Is(err, gomsg.ErrNoRoute) {
				t.Errorf("Consume for an unknown authority: %v", err)
			}
			if _, err := s.Subscribe(ctx, agent("nowhere", "y"), gomsg.Filter{}); !errors.Is(err, gomsg.ErrNoRoute) {
				t.Errorf("Subscribe for an unknown authority: %v", err)
			}
			// local authorities are served locally
			for _, a := range locals {
				if _, err := s.Send(ctx, notice(agent(a, "x"), agent(a, "z"))); err != nil {
					t.Errorf("local %s: %v", a, err)
				}
			}
		})
	}
}

func TestOutboundStoreHonorsTheConfiguredOpSet(t *testing.T) {
	ins := startInstalls(t, "", []string{"alpha"}, []string{"beta"})
	if _, err := ins[0].f.Store().Inbox(context.Background(), agent("beta", "y"), gomsg.Filter{}); !errors.Is(err, httpstore.ErrUnsupported) {
		t.Errorf("Inbox across the default hop: %v", err)
	}
	wide := startInstalls(t, `"ops": ["send","get","thread","consume","cancel","inbox","subscribe"],`, []string{"alpha"}, []string{"beta"})
	if got := wide[1].f.Server().Ops(); !got.Allows(OpInbox) || !got.Allows(OpSubscribe) {
		t.Errorf("the config's ops must reach the server: %v", got)
	}
}

func TestFederationAccessors(t *testing.T) {
	ins := startInstalls(t, "", []string{"alpha", "alpha2"}, []string{"beta"})
	f := ins[0].f
	if f.ListenAddr() != ins[0].addr || len(f.LocalAuthorities()) != 2 || f.Server() == nil {
		t.Errorf("accessors: %s %v", f.ListenAddr(), f.LocalAuthorities())
	}
	f.LocalAuthorities()[0] = "tampered"
	if f.LocalAuthorities()[0] != "alpha" {
		t.Error("LocalAuthorities must return a copy")
	}
}
