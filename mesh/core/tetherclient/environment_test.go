package tether

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testEnvironment(t *testing.T, routes []string, opts EnvironmentOptions) *EnvironmentClient {
	t.Helper()
	if opts.ResolveCredential == nil {
		opts.ResolveCredential = func(context.Context, string) (string, error) { return "test-secret", nil }
	}
	target := EnvironmentTarget{EnvironmentID: "env-test", Authority: "test authority", CredentialReference: "test-reference"}
	for _, route := range routes {
		target.Routes = append(target.Routes, EnvironmentRoute{BaseURL: route})
	}
	client, err := NewEnvironmentClient(target, opts)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeTestDescriptor(w http.ResponseWriter, environmentID string, protocol int) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(EnvironmentDescriptor{EnvironmentID: environmentID, Protocol: protocol})
}

func testEnvironmentServer(t *testing.T, protected http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == EnvironmentDescriptorPath {
			if r.Header.Get("Authorization") != "" {
				t.Error("credential on public descriptor")
			}
			writeTestDescriptor(w, "env-test", 1)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("Tether-Protocol") != "1" {
			t.Error("missing credential or protocol")
		}
		if r.URL.Path == "/auth/context" {
			w.WriteHeader(http.StatusOK)
			return
		}
		protected(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestEnvironmentDescriptorBeforeCredential(t *testing.T) {
	for _, tc := range []struct {
		name, environmentID string
		protocol            int
	}{
		{"wrong-environment", "another-environment", 1}, {"incompatible", "env-test", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("credential leaked")
				}
				if r.URL.Path != EnvironmentDescriptorPath {
					t.Error("authenticated wrong target")
				}
				writeTestDescriptor(w, tc.environmentID, tc.protocol)
			}))
			defer s.Close()
			client := testEnvironment(t, []string{s.URL}, EnvironmentOptions{ResolveCredential: func(context.Context, string) (string, error) { calls.Add(1); return "test-secret", nil }})
			_, err := client.Connect(context.Background())
			if err == nil || calls.Load() != 0 {
				t.Fatalf("err=%v credential resolutions=%d", err, calls.Load())
			}
			if tc.protocol != 1 {
				var mismatch *ProtocolMismatchError
				if !errors.As(err, &mismatch) || mismatch.RequiredProtocol != 2 || mismatch.UpdateHint == "" {
					t.Fatalf("missing typed update hint: %v", err)
				}
			}
		})
	}
}

func TestEnvironmentOrderedRoutesAndSilentSecondPass(t *testing.T) {
	var probes, auth atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == EnvironmentDescriptorPath {
			if probes.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			writeTestDescriptor(w, "env-test", 1)
			return
		}
		auth.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer first.Close()
	var secondAuth atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == EnvironmentDescriptorPath {
			writeTestDescriptor(w, "env-test", 1)
			return
		}
		secondAuth.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer second.Close()
	client := testEnvironment(t, []string{first.URL, second.URL}, EnvironmentOptions{})
	conn, err := client.Connect(context.Background())
	if err != nil || conn.RouteIndex != 0 || probes.Load() != 2 || auth.Load() != 1 || secondAuth.Load() != 1 {
		t.Fatalf("conn=%v err=%v probes=%d firstAuth=%d secondAuth=%d", conn, err, probes.Load(), auth.Load(), secondAuth.Load())
	}
}

func TestEnvironmentPreferenceAndMismatchStopsWalk(t *testing.T) {
	var fallbackAuth atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == EnvironmentDescriptorPath {
			writeTestDescriptor(w, "env-test", 1)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"error":{"code":"protocol_mismatch","required_protocol":2,"update_hint":"upgrade test-secret"}}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == EnvironmentDescriptorPath {
			writeTestDescriptor(w, "env-test", 1)
			return
		}
		fallbackAuth.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()
	_, err := testEnvironment(t, []string{first.URL, second.URL}, EnvironmentOptions{}).Connect(context.Background())
	var mismatch *ProtocolMismatchError
	if !errors.As(err, &mismatch) || mismatch.UpdateHint != "upgrade [redacted]" || fallbackAuth.Load() != 0 {
		t.Fatalf("error=%v fallbackAuth=%d", err, fallbackAuth.Load())
	}
}

func TestEnvironmentNeverFollowsRedirectsOrAmbientCredentials(t *testing.T) {
	t.Setenv("TETHER_TOKEN", "ambient-secret")
	var redirected, resolved atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(http.StatusOK) }))
	defer destination.Close()
	for _, redirectDescriptor := range []bool{true, false} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == EnvironmentDescriptorPath && !redirectDescriptor {
				writeTestDescriptor(w, "env-test", 1)
				return
			}
			if r.URL.Path == EnvironmentDescriptorPath && r.Header.Get("Authorization") != "" {
				t.Error("ambient credential used")
			}
			http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
		}))
		client := testEnvironment(t, []string{s.URL}, EnvironmentOptions{ResolveCredential: func(_ context.Context, reference string) (string, error) {
			if reference != "test-reference" {
				t.Error("wrong reference")
			}
			resolved.Add(1)
			return "test-secret", nil
		}})
		_, err := client.Connect(context.Background())
		if err == nil {
			t.Fatal("redirect succeeded")
		}
		s.Close()
	}
	if redirected.Load() != 0 || resolved.Load() != 1 {
		t.Fatalf("redirect=%d resolved=%d", redirected.Load(), resolved.Load())
	}
}

func TestEnvironmentDiscoveryExcludesCallerCookies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("ambient cookie credential sent")
		}
		if r.URL.Path == EnvironmentDescriptorPath {
			writeTestDescriptor(w, "env-test", 1)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse(srv.URL)
	jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "ambient-secret"}})
	hc := &http.Client{Jar: jar}
	_, err := testEnvironment(t, []string{srv.URL}, EnvironmentOptions{HTTPClient: hc}).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hc.Jar.Cookies(origin)) != 1 {
		t.Fatal("modified caller cookie jar")
	}
}

func TestEnvironmentExplicitTargetAndBoundedDiscovery(t *testing.T) {
	for _, route := range []string{"", "unix:/run/tether.sock", "http://secret@example.test", "http://example.test?secret=x", "http://example.test/#secret"} {
		_, err := NewEnvironmentClient(EnvironmentTarget{EnvironmentID: "e", Authority: "a", Routes: []EnvironmentRoute{{BaseURL: route}}, CredentialReference: "r"}, EnvironmentOptions{ResolveCredential: func(context.Context, string) (string, error) { return "secret", nil }})
		if err == nil {
			t.Fatalf("accepted invalid remote route %q", route)
		}
	}
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	client := testEnvironment(t, []string{s.URL}, EnvironmentOptions{ProbeTimeout: 5 * time.Millisecond, ConnectTimeout: 5 * time.Millisecond, ResolveCredential: func(context.Context, string) (string, error) { calls.Add(1); return "secret", nil }})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := client.Connect(ctx)
	if err == nil || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestEnvironmentMutationIsNotReplayed(t *testing.T) {
	var mutations atomic.Int32
	s := testEnvironmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sessions" {
			t.Error("unexpected mutation")
		}
		var req LaunchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.IdempotencyKey != "caller-key" {
			t.Error("idempotency key lost")
		}
		mutations.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	conn, err := testEnvironment(t, []string{s.URL}, EnvironmentOptions{}).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Client.CreateSessionWithInput(context.Background(), LaunchRequest{IdempotencyKey: "caller-key"})
	if err == nil || mutations.Load() != 1 {
		t.Fatalf("err=%v mutations=%d", err, mutations.Load())
	}
}

func TestEnvironmentKeyedMutationIsNotTransportReplayed(t *testing.T) {
	var mutations atomic.Int32
	srv := testEnvironmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") != "caller-key" {
			t.Error("missing keyed mutation")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		if mutations.Add(1) == 1 {
			// The server has received the mutation but loses its response. Go's
			// default transport replays a keyed POST on a reused connection.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{}`)
	})
	conn, err := testEnvironment(t, []string{srv.URL}, EnvironmentOptions{}).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Client.Reply(context.Background(), "message-test", "body", ReplyOptions{IdempotencyKey: "caller-key"})
	if err == nil || mutations.Load() != 1 {
		t.Fatalf("mutation outcome=%v attempts=%d; mutation must be caller-retried", err, mutations.Load())
	}
}

func TestEnvironmentTLSReadsRetainHTTP2AndMutationsUseHTTP1(t *testing.T) {
	var readProtocol, writeProtocol atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == EnvironmentDescriptorPath {
			if r.Header.Get("Authorization") != "" {
				t.Error("credential on TLS discovery")
			}
			writeTestDescriptor(w, "env-test", 1)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("Tether-Protocol") != "1" {
			t.Error("missing TLS authentication/protocol")
		}
		if r.URL.Path == "/auth/context" {
			readProtocol.Store(int32(r.ProtoMajor))
			return
		}
		writeProtocol.Store(int32(r.ProtoMajor))
		if r.Header.Get("Idempotency-Key") != "caller-key" {
			t.Error("lost caller key")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{}`)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	hc := srv.Client()
	base := hc.Transport.(*http.Transport).Clone()
	base.ForceAttemptHTTP2 = true
	base.Protocols = new(http.Protocols)
	base.Protocols.SetHTTP1(true)
	base.Protocols.SetHTTP2(true)
	hc.Transport = base
	conn, err := testEnvironment(t, []string{srv.URL}, EnvironmentOptions{HTTPClient: hc}).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Client.Reply(context.Background(), "message-test", "body", ReplyOptions{IdempotencyKey: "caller-key"})
	if err != nil {
		t.Fatal(err)
	}
	if readProtocol.Load() != 2 || writeProtocol.Load() != 1 {
		t.Fatalf("read HTTP/%d mutation HTTP/%d", readProtocol.Load(), writeProtocol.Load())
	}
	if base.DisableKeepAlives || !base.Protocols.HTTP2() {
		t.Fatal("modified caller transport")
	}
}

func TestEnvironmentErrorsDoNotEchoSecrets(t *testing.T) {
	client := testEnvironment(t, []string{"http://example.test"}, EnvironmentOptions{HTTPClient: &http.Client{Transport: environmentTestTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("test-secret private transport detail")
	})}})
	_, err := client.Connect(context.Background())
	if err == nil || strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe error %v", err)
	}
}

type environmentTestTransport func(*http.Request) (*http.Response, error)

func (f environmentTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
