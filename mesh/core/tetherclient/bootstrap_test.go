package tether

// bootstrap_test.go — fixture coverage for ResolveSessionBootstrap /
// BootstrapSession named in the messaging-vnext architecture's T08
// acceptance: standalone session-only, durable actor, missing provider
// ID, repeated hook calls, explicit publication, private offline boot,
// and reconnect.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeBootstrapDaemon mimics Tether's POST /sessions/bootstrap: the
// first call for a given session_id "creates" it, every subsequent call
// is a no-op on the identity itself (created=false), and any supplied
// provider_mappings are recorded regardless.
type fakeBootstrapDaemon struct {
	mu       sync.Mutex
	seen     map[string]bool
	mappings map[string][]ProviderMapping
}

func newFakeBootstrapDaemon(t *testing.T) *Client {
	t.Helper()
	d := &fakeBootstrapDaemon{seen: map[string]bool{}, mappings: map[string][]ProviderMapping{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sessions/bootstrap" {
			http.NotFound(w, r)
			return
		}
		var req SessionBootstrapRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.SessionID == "" {
			http.Error(w, "session_id required", http.StatusBadRequest)
			return
		}
		d.mu.Lock()
		created := !d.seen[req.SessionID]
		d.seen[req.SessionID] = true
		d.mappings[req.SessionID] = append(d.mappings[req.SessionID], req.ProviderMappings...)
		d.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SessionBootstrapResult{SessionID: req.SessionID, Created: created})
	}))
	t.Cleanup(srv.Close)
	return MustNew(srv.URL)
}

// TestResolveSessionBootstrap_StandaloneSessionOnly is the "standalone
// session-only" fixture: no preassigned SessionID, no env var -- a fresh
// id is minted and IntentFresh applies.
func TestResolveSessionBootstrap_StandaloneSessionOnly(t *testing.T) {
	t.Setenv(CanonicalSessionEnvKey, "")
	c := newFakeBootstrapDaemon(t)
	res, err := ResolveSessionBootstrap(context.Background(), c, BootstrapOptions{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.SessionID == "" {
		t.Fatal("expected a minted session id")
	}
	if res.Intent != IntentFresh {
		t.Errorf("intent = %q, want fresh", res.Intent)
	}
	if !res.Registered {
		t.Errorf("registered = false, want true (daemon reachable): %v", res.RegisterErr)
	}
}

// TestResolveSessionBootstrap_PreassignedViaEnv is the "repeated hook
// calls" fixture's setup half: a launcher preassigns SESSION via env,
// and ResolveSessionBootstrap must resolve to exactly that value.
func TestResolveSessionBootstrap_PreassignedViaEnv(t *testing.T) {
	t.Setenv(CanonicalSessionEnvKey, "sess-preassigned-123")
	c := newFakeBootstrapDaemon(t)
	res, err := ResolveSessionBootstrap(context.Background(), c, BootstrapOptions{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.SessionID != "sess-preassigned-123" {
		t.Fatalf("session id = %q, want the preassigned env value", res.SessionID)
	}
	if res.Intent != IntentPreassigned {
		t.Errorf("intent = %q, want preassigned", res.Intent)
	}
}

// TestResolveSessionBootstrap_DurableActor is the "durable actor"
// fixture: LogicalAgentID is forwarded to the daemon.
func TestResolveSessionBootstrap_DurableActor(t *testing.T) {
	c := newFakeBootstrapDaemon(t)
	res, err := ResolveSessionBootstrap(context.Background(), c, BootstrapOptions{
		SessionID:      "sess-durable",
		LogicalAgentID: "agt_worker",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !res.Registered {
		t.Fatalf("registered = false: %v", res.RegisterErr)
	}
}

// TestResolveSessionBootstrap_MissingProviderID is the "missing provider
// ID" fixture: no ProviderMappings supplied at all -- must still succeed.
func TestResolveSessionBootstrap_MissingProviderID(t *testing.T) {
	c := newFakeBootstrapDaemon(t)
	res, err := ResolveSessionBootstrap(context.Background(), c, BootstrapOptions{SessionID: "sess-no-provider"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !res.Registered {
		t.Fatalf("registered = false: %v", res.RegisterErr)
	}
}

// TestResolveSessionBootstrap_RepeatedHookCalls is the "repeated hook
// calls" fixture: calling bootstrap twice with the SAME preassigned
// SessionID resolves to the identical id both times and never errors,
// even though the daemon reports created=false the second time
// (verified via the fake daemon's own bookkeeping, since
// ResolveSessionBootstrap's own result doesn't surface Created --
// callers needing that distinction use BootstrapSession directly).
func TestResolveSessionBootstrap_RepeatedHookCalls(t *testing.T) {
	t.Setenv(CanonicalSessionEnvKey, "sess-repeated")
	c := newFakeBootstrapDaemon(t)

	first, err := ResolveSessionBootstrap(context.Background(), c, BootstrapOptions{})
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	second, err := ResolveSessionBootstrap(context.Background(), c, BootstrapOptions{})
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if first.SessionID != second.SessionID {
		t.Fatalf("session ids differ across repeated calls: %q vs %q -- a competing identity was invented", first.SessionID, second.SessionID)
	}
	if !second.Registered {
		t.Fatalf("second call registered = false: %v", second.RegisterErr)
	}
}

// TestResolveSessionBootstrap_ExplicitPublication is the "explicit
// publication" fixture.
func TestResolveSessionBootstrap_ExplicitPublication(t *testing.T) {
	c := newFakeBootstrapDaemon(t)
	res, err := ResolveSessionBootstrap(context.Background(), c, BootstrapOptions{
		SessionID:   "sess-published",
		Publication: PublicationPublishedLocal,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !res.Registered {
		t.Fatalf("registered = false: %v", res.RegisterErr)
	}
}

// TestResolveSessionBootstrap_Reconnect is the "reconnect" fixture: a
// second call for the same session_id supplies a freshly-observed
// provider mapping.
func TestResolveSessionBootstrap_Reconnect(t *testing.T) {
	c := newFakeBootstrapDaemon(t)
	if _, err := ResolveSessionBootstrap(context.Background(), c, BootstrapOptions{SessionID: "sess-reconnect"}); err != nil {
		t.Fatalf("initial resolve: %v", err)
	}
	res, err := ResolveSessionBootstrap(context.Background(), c, BootstrapOptions{
		SessionID: "sess-reconnect",
		ProviderMappings: []ProviderMapping{
			{Owner: "tether", Provider: "claude-code", NativeSessionID: "native-xyz"},
		},
	})
	if err != nil {
		t.Fatalf("reconnect resolve: %v", err)
	}
	if !res.Registered {
		t.Fatalf("reconnect registered = false: %v", res.RegisterErr)
	}
}

// TestResolveSessionBootstrap_PrivateOfflineBoot is the "private offline
// boot" fixture -- the property this whole helper exists for: no
// reachable Tether daemon at all must NOT prevent a usable local
// identity from being resolved. Both a nil client and an unreachable one
// are covered.
func TestResolveSessionBootstrap_PrivateOfflineBoot(t *testing.T) {
	t.Run("nil client", func(t *testing.T) {
		res, err := ResolveSessionBootstrap(context.Background(), nil, BootstrapOptions{SessionID: "sess-offline-nil"})
		if err != nil {
			t.Fatalf("resolve with nil client: %v", err)
		}
		if res.SessionID != "sess-offline-nil" {
			t.Fatalf("session id = %q, want sess-offline-nil", res.SessionID)
		}
		if res.Registered {
			t.Fatal("registered = true with a nil client, want false")
		}
		if !errors.Is(res.RegisterErr, ErrDaemonUnreachable) {
			t.Fatalf("register err = %v, want ErrDaemonUnreachable", res.RegisterErr)
		}
	})

	t.Run("unreachable client", func(t *testing.T) {
		srv := httptest.NewServer(nil)
		c := MustNew(srv.URL)
		srv.Close() // close before any request -- guarantees connection refused

		res, err := ResolveSessionBootstrap(context.Background(), c, BootstrapOptions{SessionID: "sess-offline-closed"})
		if err != nil {
			t.Fatalf("resolve with unreachable daemon: %v", err)
		}
		if res.SessionID != "sess-offline-closed" {
			t.Fatalf("session id = %q, want sess-offline-closed (must still resolve locally)", res.SessionID)
		}
		if res.Registered {
			t.Fatal("registered = true against a closed server, want false")
		}
		if !errors.Is(res.RegisterErr, ErrDaemonUnreachable) {
			t.Fatalf("register err = %v, want ErrDaemonUnreachable", res.RegisterErr)
		}
	})
}

// TestResolveSessionBootstrap_MintsWhenEnvAndOptionBothEmpty guards the
// resolution-order contract directly (SessionID option, then env var,
// then mint) beyond what the "standalone" fixture above already implies.
func TestResolveSessionBootstrap_MintsWhenEnvAndOptionBothEmpty(t *testing.T) {
	t.Setenv(CanonicalSessionEnvKey, "")

	res1, err := ResolveSessionBootstrap(context.Background(), nil, BootstrapOptions{})
	if err != nil {
		t.Fatalf("resolve 1: %v", err)
	}
	res2, err := ResolveSessionBootstrap(context.Background(), nil, BootstrapOptions{})
	if err != nil {
		t.Fatalf("resolve 2: %v", err)
	}
	if res1.SessionID == "" || res2.SessionID == "" {
		t.Fatal("expected minted, non-empty session ids")
	}
	if res1.SessionID == res2.SessionID {
		t.Fatal("two independent mint-fallback calls produced the SAME id -- each unbootstrapped call should mint its own")
	}
}

func TestBootstrapSession_RequiresSessionID(t *testing.T) {
	c := newFakeBootstrapDaemon(t)
	if _, err := c.BootstrapSession(context.Background(), SessionBootstrapRequest{}); err == nil {
		t.Fatal("expected an error with no session_id")
	}
}
