package federation

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode"

	gomsg "github.com/hollis-labs/go-messaging"
	"github.com/hollis-labs/go-messaging/httpstore"
)

// fixture: a server homing "local" that admits one peer, authoritative for "remote".
type fixture struct {
	*hop
	remote tls.Certificate
	raw    *http.Client
}

func newFixture(t *testing.T, ops OpSet, local []string, extra ...ServerOption) *fixture {
	t.Helper()
	remote := validIdentity(t)
	if local == nil {
		local = []string{"local"}
	}
	h := startHop(t, ops, local, []PeerConfig{peer("remote-install", remote, "remote", "shared")}, extra...)
	raw, err := ClientHTTPClient(remote, []string{fp(h.identity)})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{hop: h, remote: remote, raw: raw}
}

func (f *fixture) do(method, path, body string) (int, string) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.url+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	resp, err := f.raw.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func mustSend(t *testing.T, s gomsg.Store, e gomsg.Envelope) gomsg.Envelope {
	t.Helper()
	out, err := s.Send(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func envJSON(t *testing.T, e gomsg.Envelope) string {
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSendDeliversForAPeerOfTheSenderAuthority(t *testing.T) {
	f := newFixture(t, nil, nil)
	c := f.dial(f.remote)
	sent, err := c.Send(context.Background(), notice(agent("remote", "a"), agent("local", "b")))
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.store.Get(context.Background(), sent.ID)
	if err != nil || got.To != agent("local", "b") || got.From != agent("remote", "a") {
		t.Fatalf("stored = %+v, %v", got, err)
	}
	if rec := f.audit.last(); !rec.Allowed || rec.Peer != "remote-install" || rec.Op != OpSend || rec.Authority != "local" || rec.Fingerprint != fp(f.remote) {
		t.Errorf("audit = %+v", rec)
	}
}

func TestSendRefusals(t *testing.T) {
	f := newFixture(t, nil, nil, WithMaxPayloadBytes(64))
	tests := []struct {
		name   string
		body   string
		status int
		reason string
	}{
		{"forged sender authority", envJSON(t, notice(agent("local", "a"), agent("local", "b"))), 403, "may not originate"},
		{"impersonating a third authority", envJSON(t, notice(agent("third", "a"), agent("local", "b"))), 403, "may not originate"},
		{"relay to an authority not homed here", envJSON(t, notice(agent("remote", "a"), agent("elsewhere", "b"))), 404, "not homed"},
		{"invalid JSON", `{"kind":`, 400, ""},
		{"missing kind", envJSON(t, gomsg.Envelope{From: agent("remote", "a"), To: agent("local", "b")}), 422, ""},
		{"missing to", envJSON(t, gomsg.Envelope{Kind: gomsg.MsgKindNotice, From: agent("remote", "a")}), 422, ""},
		{"payload over the limit", func() string {
			e := notice(agent("remote", "a"), agent("local", "b"))
			e.Payload = json.RawMessage(`"` + strings.Repeat("x", 100) + `"`)
			return envJSON(t, e)
		}(), 413, "payload over"},
		{"body over the limit", `{"kind":"notice","payload":"` + strings.Repeat("x", 40000) + `"}`, 413, "over the limit"},
		{"reserved metadata namespace", func() string {
			e := notice(agent("remote", "a"), agent("local", "b"))
			e.Metadata = map[string]string{"fed.sig": "forged"}
			return envJSON(t, e)
		}(), 422, "reserved"},
		{"preset lifecycle", func() string {
			e := notice(agent("remote", "a"), agent("local", "b"))
			now := time.Now()
			e.DeliveredAt = &now
			return envJSON(t, e)
		}(), 422, ""},
	}
	for _, tc := range tests {
		before := f.store.count("send")
		status, body := f.do(http.MethodPost, BasePath, tc.body)
		if status != tc.status {
			t.Errorf("%s: status %d (%s), want %d", tc.name, status, body, tc.status)
		}
		if status >= 400 && status != 422 && f.store.count("send") != before && status != 400 {
			t.Errorf("%s: a refused Send reached the store", tc.name)
		}
		if tc.reason != "" && !strings.Contains(f.audit.last().Reason, tc.reason) {
			t.Errorf("%s: audit reason %q lacks %q", tc.name, f.audit.last().Reason, tc.reason)
		}
		// the caller is told nothing about why beyond the fixed phrase
		if strings.Contains(body, "authoritative for") || strings.Contains(body, "remote-install") {
			t.Errorf("%s: the response leaks authorization detail: %s", tc.name, body)
		}
	}
	for _, name := range []string{"forged sender authority", "impersonating a third authority", "relay to an authority not homed here"} {
		_ = name
	}
	if n := f.store.count("send"); n != 1 {
		// only the preset-lifecycle case is allowed to reach the store (it is the store that rejects it)
		t.Errorf("%d sends reached the store, want 1 (the lifecycle case, rejected by the store)", n)
	}
}

func TestGetConsumeCancelRequireBeingAParty(t *testing.T) {
	f := newFixture(t, nil, nil)
	ctx := context.Background()
	mine := mustSend(t, f.store, notice(agent("remote", "a"), agent("local", "b")))
	foreign := mustSend(t, f.store, notice(agent("local", "a"), agent("local", "b")))

	c := f.dial(f.remote)
	if got, err := c.Get(ctx, mine.ID); err != nil || got.ID != mine.ID {
		t.Fatalf("Get own = %+v, %v", got, err)
	}
	if _, err := c.Get(ctx, foreign.ID); err == nil {
		t.Fatal("a peer must not read an envelope it is not a party to")
	}
	if status, _ := f.do(http.MethodGet, BasePath+"/"+foreign.ID, ""); status != 404 {
		t.Errorf("Get non-party: %d, want 404 (the same answer as a missing id)", status)
	}
	if status, _ := f.do(http.MethodGet, BasePath+"/does-not-exist", ""); status != 404 {
		t.Errorf("Get unknown: %d", status)
	}

	if err := c.Consume(ctx, mine.ID, mine.To); err != nil {
		t.Fatalf("Consume own: %v", err)
	}
	if err := c.Consume(ctx, foreign.ID, foreign.To); err == nil {
		t.Error("Consume of an envelope the peer is not a party to")
	}
	status, _ := f.do(http.MethodPost, BasePath+"/"+mine.ID+"/consume", `{"recipient":"`+agent("local", "somebody-else").URN()+`"}`)
	if status != 403 {
		t.Errorf("Consume for an unrelated recipient: %d, want 403", status)
	}
	for name, body := range map[string]string{"bad JSON": `{`, "no recipient": `{}`, "bad URN": `{"recipient":"nope"}`} {
		want := map[string]int{"bad JSON": 400, "no recipient": 422, "bad URN": 422}[name]
		if status, _ := f.do(http.MethodPost, BasePath+"/"+mine.ID+"/consume", body); status != want {
			t.Errorf("Consume %s: %d, want %d", name, status, want)
		}
	}

	if err := c.Cancel(ctx, foreign.ID); err == nil {
		t.Error("Cancel of an envelope the peer is not a party to")
	}
	if got, _ := f.store.Get(ctx, foreign.ID); got.ID == "" {
		t.Fatal("the store lost the envelope")
	}
	if err := c.Cancel(ctx, mine.ID); err != nil {
		t.Errorf("Cancel own: %v", err)
	}
}

func TestThreadIsFilteredToTheCallersEnvelopes(t *testing.T) {
	f := newFixture(t, nil, nil)
	ctx := context.Background()
	mine := notice(agent("remote", "a"), agent("local", "b"))
	mine.ThreadID = "T"
	private := notice(agent("local", "x"), agent("local", "y"))
	private.ThreadID = "T"
	mustSend(t, f.store, mine)
	mustSend(t, f.store, private)

	got, err := f.dial(f.remote).Thread(ctx, "T", gomsg.Filter{})
	if err != nil || len(got) != 1 || got[0].From != mine.From {
		t.Fatalf("Thread = %+v, %v; the peer must see only its own envelope", got, err)
	}
	// a thread the peer has no part in, and one that does not exist, answer the same: 200 and nothing
	other := notice(agent("local", "x"), agent("local", "y"))
	other.ThreadID = "PRIVATE"
	mustSend(t, f.store, other)
	s1, b1 := f.do(http.MethodGet, BasePath+"/thread/PRIVATE", "")
	s2, b2 := f.do(http.MethodGet, BasePath+"/thread/NEVER-EXISTED", "")
	if s1 != 200 || s2 != 200 || b1 != b2 || !strings.Contains(b1, `"messages":[]`) {
		t.Errorf("the answer must not tell a peer whether a thread exists: %d %q / %d %q", s1, b1, s2, b2)
	}
}

func TestListLimitsAreClamped(t *testing.T) {
	f := newFixture(t, nil, nil, WithMaxListLimit(2))
	for i := 0; i < 5; i++ {
		e := notice(agent("remote", "a"), agent("local", "b"))
		e.ThreadID = "T"
		mustSend(t, f.store, e)
	}
	count := func(query string) int {
		status, body := f.do(http.MethodGet, BasePath+"/thread/T"+query, "")
		if status != 200 {
			t.Fatalf("%s: %d %s", query, status, body)
		}
		var out struct{ Messages []gomsg.Envelope }
		_ = json.Unmarshal([]byte(body), &out)
		return len(out.Messages)
	}
	if got := count(""); got != 2 {
		t.Errorf("no limit: %d, want the server maximum", got)
	}
	if got := count("?limit=1"); got != 1 {
		t.Errorf("limit=1: %d", got)
	}
	if got := count("?limit=1000000"); got != 2 {
		t.Errorf("an oversized limit must be clamped: %d", got)
	}
	if status, _ := f.do(http.MethodGet, BasePath+"/thread/T?limit=abc", ""); status != 400 {
		t.Errorf("limit=abc: %d", status)
	}
	if status, _ := f.do(http.MethodGet, BasePath+"/thread/T?limit=-1", ""); status != 400 {
		t.Errorf("limit=-1: %d", status)
	}
}

// Inbox and Subscribe are off by default. "Off" is a route that does not exist and
// a store that is never touched, whatever the caller tries.
func TestDefaultOpSetDoesNotMountInboxOrSubscribe(t *testing.T) {
	f := newFixture(t, nil, nil)
	to := agent("shared", "u").URN()
	for _, path := range []string{"/inbox?to=" + to, "/subscribe?to=" + to} {
		if status, _ := f.do(http.MethodGet, BasePath+path, ""); status != 404 {
			t.Errorf("GET %s: %d, want 404", path, status)
		}
	}
	if f.store.count("inbox") != 0 || f.store.count("subscribe") != 0 {
		t.Fatal("a disabled operation must never reach the store")
	}

	// and the client, given the default set, refuses without a request
	hits := 0
	c := f.dial(f.remote, WithHTTPStoreOptions(httpstore.WithRequestHook(func(*http.Request, httpstore.Op) error { hits++; return nil })))
	if _, err := c.Inbox(context.Background(), agent("shared", "u"), gomsg.Filter{}); !errors.Is(err, httpstore.ErrUnsupported) || !errors.Is(err, gomsg.ErrStoreUnavailable) {
		t.Errorf("Inbox: %v", err)
	}
	if ch, err := c.Subscribe(context.Background(), agent("shared", "u"), gomsg.Filter{}); !errors.Is(err, httpstore.ErrUnsupported) || ch != nil {
		t.Errorf("Subscribe: %v", err)
	}
	if hits != 0 {
		t.Errorf("%d requests were sent for unsupported operations", hits)
	}
}

func TestDisabledOperationsAreNotMounted(t *testing.T) {
	only, _ := NewOpSet(OpSend)
	f := newFixture(t, only, nil)
	mine := mustSend(t, f.store, notice(agent("remote", "a"), agent("local", "b")))
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, BasePath + "/" + mine.ID},
		{http.MethodGet, BasePath + "/thread/T"},
		{http.MethodPost, BasePath + "/" + mine.ID + "/consume"},
		{http.MethodPost, BasePath + "/" + mine.ID + "/cancel"},
	} {
		if status, _ := f.do(tc.method, tc.path, `{"recipient":"x"}`); status != 404 && status != 405 {
			t.Errorf("%s %s: %d, want the route absent", tc.method, tc.path, status)
		}
	}
	if f.store.count("get")+f.store.count("thread")+f.store.count("consume")+f.store.count("cancel") != 0 {
		t.Error("a disabled operation reached the store")
	}
	if _, err := f.dial(f.remote, WithOpSet(only)).Send(context.Background(), notice(agent("remote", "a"), agent("local", "b"))); err != nil {
		t.Errorf("the one enabled operation works: %v", err)
	}
}

func TestWidenedOpSetServesInboxAndSubscribeForSharedAuthorities(t *testing.T) {
	wide, err := DefaultOpSet().Widen(OpInbox, OpSubscribe)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, wide, []string{"local", "shared"})
	c := f.dial(f.remote, WithOpSet(wide))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	to := agent("shared", "u")
	mustSend(t, f.store, notice(agent("local", "a"), to))
	got, err := c.Inbox(ctx, to, gomsg.Filter{})
	if err != nil || len(got) != 1 {
		t.Fatalf("Inbox = %+v, %v", got, err)
	}
	// a mailbox of an authority the peer is not authoritative for stays closed
	mustSend(t, f.store, notice(agent("local", "a"), agent("local", "u")))
	if _, ierr := c.Inbox(ctx, agent("local", "u"), gomsg.Filter{}); ierr == nil {
		t.Error("a peer must not drain a mailbox of an authority it does not hold")
	}
	if status, _ := f.do(http.MethodGet, BasePath+"/inbox?to="+agent("local", "u").URN(), ""); status != 403 {
		t.Errorf("Inbox for a foreign authority: %d, want 403", status)
	}
	for name, q := range map[string]string{"missing to": "", "bad URN": "?to=zzz"} {
		want := map[string]int{"missing to": 400, "bad URN": 422}[name]
		if status, _ := f.do(http.MethodGet, BasePath+"/inbox"+q, ""); status != want {
			t.Errorf("Inbox %s: %d, want %d", name, status, want)
		}
	}

	ch, err := c.Subscribe(ctx, to, gomsg.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	live := mustSend(t, f.store, notice(agent("local", "a"), to))
	select {
	case env := <-ch:
		if env.ID != live.ID {
			t.Fatalf("got %s, want %s", env.ID, live.ID)
		}
	case <-ctx.Done():
		t.Fatal("no envelope arrived on the stream")
	}
	if status, _ := f.do(http.MethodGet, BasePath+"/subscribe?to="+agent("local", "u").URN(), ""); status != 403 {
		t.Errorf("Subscribe for a foreign authority: %d, want 403", status)
	}
}

func TestSubscribeStreamsFramesAndEndsWithTheRequest(t *testing.T) {
	wide, _ := DefaultOpSet().Widen(OpSubscribe)
	f := newFixture(t, wide, []string{"local", "shared"})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.url+BasePath+"/subscribe?to="+agent("shared", "u").URN(), nil)
	resp, err := f.raw.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	r := bufio.NewReader(resp.Body)
	if line, _ := r.ReadString('\n'); !strings.HasPrefix(line, ":") {
		t.Fatalf("first line = %q, want the subscribed comment", line)
	}
	cancel()
	// the stream ends once the request context is gone
	_, _ = io.Copy(io.Discard, resp.Body)
}

func TestErrorsAndPanicsNeverLeakInternals(t *testing.T) {
	f := newFixture(t, nil, nil)
	mine := mustSend(t, f.store, notice(agent("remote", "a"), agent("local", "b")))

	f.store.err = errors.New("open /var/lib/secret/messages.db: permission denied")
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, BasePath + "/" + mine.ID, ""},
		{http.MethodGet, BasePath + "/thread/T", ""},
		{http.MethodPost, BasePath, envJSON(t, notice(agent("remote", "a"), agent("local", "b")))},
	} {
		status, body := f.do(tc.method, tc.path, tc.body)
		if status != 500 || strings.Contains(body, "secret") || strings.Contains(body, "messages.db") {
			t.Errorf("%s %s: %d %q", tc.method, tc.path, status, body)
		}
		if !strings.Contains(f.audit.last().Reason, "messages.db") {
			t.Errorf("the audit log must keep the detail: %q", f.audit.last().Reason)
		}
	}
	f.store.err = nil

	f.store.panic = true
	status, body := f.do(http.MethodGet, BasePath+"/"+mine.ID, "")
	if status != 500 || strings.Contains(body, "secret-internal-detail") {
		t.Errorf("panic: %d %q", status, body)
	}
	f.store.mu.Lock()
	f.store.panic = false
	f.store.mu.Unlock()
	if status, _ := f.do(http.MethodGet, BasePath+"/"+mine.ID, ""); status != 200 {
		t.Errorf("the server must keep serving after a handler panic: %d", status)
	}
}

// The handler answers 401 for a request with no client certificate and 403 for one
// the resolver does not know: the identity is decided per request, from the
// resolver, whatever the TLS config did.
func TestUnauthenticatedAndUnknownCallersAreRefused(t *testing.T) {
	reg, _ := NewPeerRegistry([]PeerConfig{peer("p", validIdentity(t), "remote")})
	audit := &auditLog{}
	store := newSpy()
	s, err := NewServer(store, MTLSPinnedResolver(reg), nil, []string{"local"}, WithAuditor(audit.add))
	if err != nil {
		t.Fatal(err)
	}
	do := func(r *http.Request) (int, string) {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	body := envJSON(t, notice(agent("remote", "a"), agent("local", "b")))

	plain := httptest.NewRequest(http.MethodPost, BasePath, strings.NewReader(body))
	if status, resp := do(plain); status != 401 || strings.Contains(resp, "certificate") {
		t.Errorf("plain HTTP: %d %q", status, resp)
	}
	stranger := httptest.NewRequest(http.MethodPost, BasePath, strings.NewReader(body))
	stranger.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{validIdentity(t).Leaf}}
	if status, _ := do(stranger); status != 403 {
		t.Errorf("unpinned certificate: %d", status)
	}
	if store.count("send") != 0 {
		t.Fatal("an unauthenticated request reached the store")
	}
	if rec := audit.last(); rec.Allowed || rec.Peer != "<unknown>" || !strings.Contains(rec.Reason, "identity") {
		t.Errorf("audit = %+v", rec)
	}
}

func TestNewServerRefusesMisconfiguration(t *testing.T) {
	reg, _ := NewPeerRegistry([]PeerConfig{peer("p", validIdentity(t), "a")})
	res := MTLSPinnedResolver(reg)
	st := newSpy()
	tests := map[string]func() error{
		"nil store":             func() error { _, err := NewServer(nil, res, nil, []string{"l"}); return err },
		"nil resolver":          func() error { _, err := NewServer(st, nil, nil, []string{"l"}); return err },
		"no local authority":    func() error { _, err := NewServer(st, res, nil, nil); return err },
		"empty local authority": func() error { _, err := NewServer(st, res, nil, []string{""}); return err },
		"unknown operation":     func() error { _, err := NewServer(st, res, OpSet{"nope": true}, []string{"l"}); return err },
		"no operation":          func() error { _, err := NewServer(st, res, OpSet{}, []string{"l"}); return err },
	}
	for name, f := range tests {
		if err := f(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s, err := NewServer(st, res, nil, []string{"l"})
	if err != nil || s.Ops().Allows(OpInbox) || !s.Ops().Allows(OpSend) {
		t.Fatalf("nil ops must mean DefaultOpSet: %v %v", s, err)
	}
}

func TestServeRefusesAListenerThatDoesNotRequireClientCertificates(t *testing.T) {
	reg, _ := NewPeerRegistry([]PeerConfig{peer("p", validIdentity(t), "a")})
	s, _ := NewServer(newSpy(), MTLSPinnedResolver(reg), nil, []string{"l"})
	for name, tc := range map[string]*tls.Config{"nil": nil, "no client auth": {ClientAuth: tls.NoClientCert}, "optional": {ClientAuth: tls.VerifyClientCertIfGiven}} {
		ln := listenLoopback(t)
		ctx, cancel := context.WithCancel(context.Background())
		res := make(chan error, 1)
		go func() { res <- s.Serve(ctx, ln, tc) }()
		select {
		case err := <-res:
			if err == nil || !strings.Contains(err.Error(), "client certificates") {
				t.Errorf("%s: err = %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s: Serve started a listener that does not require client certificates", name)
		}
		cancel()
	}
}

// A caller must not learn whether an id exists: an envelope it is not a party to
// answers exactly as a missing one does, for every operation that takes an id.
// The distinction is kept in the audit record only.
func TestNonPartyAndMissingIDAnswerIdentically(t *testing.T) {
	f := newFixture(t, nil, nil)
	foreign := mustSend(t, f.store, notice(agent("local", "a"), agent("local", "b")))
	consume := `{"recipient":"` + foreign.To.URN() + `"}`
	for _, tc := range []struct{ name, method, suffix, body string }{
		{"get", http.MethodGet, "", ""},
		{"consume", http.MethodPost, "/consume", consume},
		{"cancel", http.MethodPost, "/cancel", ""},
	} {
		s1, b1 := f.do(tc.method, BasePath+"/"+foreign.ID+tc.suffix, tc.body)
		reasonExisting := f.audit.last().Reason
		s2, b2 := f.do(tc.method, BasePath+"/does-not-exist"+tc.suffix, tc.body)
		if s1 != 404 || s2 != 404 || b1 != b2 {
			t.Errorf("%s: existing non-party %d %q, missing %d %q", tc.name, s1, b1, s2, b2)
		}
		if !strings.Contains(reasonExisting, "not a party") {
			t.Errorf("%s: the audit record must keep the distinction: %q", tc.name, reasonExisting)
		}
	}
	if got, _ := f.store.Get(context.Background(), foreign.ID); got.ID == "" || f.store.count("cancel") != 0 {
		t.Fatal("a refused call reached the store")
	}
}

// The store list limit must not let other parties' envelopes starve the caller's:
// the party filter is applied before the cap.
func TestThreadLimitIsAppliedAfterThePartyFilter(t *testing.T) {
	f := newFixture(t, nil, nil)
	for i := 0; i < 5; i++ {
		e := notice(agent("local", "x"), agent("local", "y"))
		e.ThreadID = "T"
		mustSend(t, f.store, e)
	}
	for i := 0; i < 2; i++ {
		e := notice(agent("remote", "a"), agent("local", "b"))
		e.ThreadID = "T"
		mustSend(t, f.store, e)
	}
	status, body := f.do(http.MethodGet, BasePath+"/thread/T?limit=3", "")
	var out struct{ Messages []gomsg.Envelope }
	_ = json.Unmarshal([]byte(body), &out)
	if status != 200 || len(out.Messages) != 2 {
		t.Fatalf("limit 3 with 5 foreign envelopes ahead: %d %q, want the caller's 2", status, body)
	}
	for _, m := range out.Messages {
		if m.From != agent("remote", "a") {
			t.Errorf("a foreign envelope leaked: %+v", m)
		}
	}
	// the cap still holds over the caller's own envelopes
	_, body = f.do(http.MethodGet, BasePath+"/thread/T?limit=1", "")
	_ = json.Unmarshal([]byte(body), &out)
	if len(out.Messages) != 1 {
		t.Errorf("limit=1: %d messages", len(out.Messages))
	}
}

func TestReservedMetadataPrefixIsRefusedWhateverItsSpelling(t *testing.T) {
	f := newFixture(t, nil, nil)
	for _, key := range []string{"fed.x", "FED.x", "Fed.x", " fed.x", "\tfed.x ", "fed\u200b.x", "f\u200ded.x", "\u200bfed.x", "fed\x00.x", "fed.\u202ex"} {
		e := notice(agent("remote", "a"), agent("local", "b"))
		e.Metadata = map[string]string{key: "forged"}
		if status, _ := f.do(http.MethodPost, BasePath, envJSON(t, e)); status != 422 {
			t.Errorf("metadata key %q: %d, want 422", key, status)
		}
	}
	e := notice(agent("remote", "a"), agent("local", "b"))
	e.Metadata = map[string]string{"feed.x": "ok", "federation": "ok"}
	if status, body := f.do(http.MethodPost, BasePath, envJSON(t, e)); status != 201 {
		t.Errorf("ordinary metadata keys: %d %s", status, body)
	}
}

// Peer-controlled text reaches the audit record; it must not carry control
// characters that could forge a log line.
func TestAuditRecordsCarryNoControlCharacters(t *testing.T) {
	f := newFixture(t, nil, nil)
	f.do(http.MethodPost, BasePath, envJSON(t, notice(agent("remote", "a"), agent("local\nforged: allow", "b"))))
	f.do(http.MethodGet, BasePath+"/thread/T%0AX%0D%1B%5Bforged", "")
	f.do(http.MethodGet, BasePath+"/id%0Aforged", "")
	recs := f.audit.all()
	if len(recs) < 3 {
		t.Fatalf("audit = %+v", recs)
	}
	for _, r := range recs {
		for _, field := range []string{r.Peer, r.Fingerprint, r.Authority, r.From, r.To, r.Reason} {
			for _, c := range field {
				if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
					t.Errorf("control character %U in audit field %q of %+v", c, field, r)
				}
			}
		}
	}
	if !strings.Contains(recs[0].To, "\uFFFD") {
		t.Errorf("To = %q, want the control character replaced", recs[0].To)
	}
}

func TestNewServerRefusesNilOptionsAndTypedNils(t *testing.T) {
	reg, _ := NewPeerRegistry([]PeerConfig{peer("p", validIdentity(t), "a")})
	res := MTLSPinnedResolver(reg)
	st := newSpy()
	var nilStore *spyStore
	var nilReg *PeerRegistry
	tests := map[string]func() error{
		"nil auditor":         func() error { _, err := NewServer(st, res, nil, []string{"l"}, WithAuditor(nil)); return err },
		"nil clock":           func() error { _, err := NewServer(st, res, nil, []string{"l"}, WithClock(nil)); return err },
		"typed-nil store":     func() error { _, err := NewServer(nilStore, res, nil, []string{"l"}); return err },
		"typed-nil registry":  func() error { _, err := NewServer(st, MTLSPinnedResolver(nilReg), nil, []string{"l"}); return err },
		"nil option function": func() error { _, err := NewServer(st, res, nil, []string{"l"}, nil); return err },
	}
	for name, f := range tests {
		func() {
			defer func() {
				if v := recover(); v != nil {
					t.Errorf("%s: panicked: %v", name, v)
				}
			}()
			if err := f(); err == nil {
				t.Errorf("%s: accepted", name)
			}
		}()
	}
}

// When the listener fails, Serve returns its error and must not leave the
// shutdown goroutine parked on a context that is never canceled.
func TestServeDoesNotLeakTheShutdownGoroutineOnAListenerError(t *testing.T) {
	reg, _ := NewPeerRegistry([]PeerConfig{peer("p", validIdentity(t), "a")})
	s, _ := NewServer(newSpy(), MTLSPinnedResolver(reg), nil, []string{"l"})
	tc, err := ServerTLSConfig(validIdentity(t), reg)
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		ln := listenLoopback(t)
		_ = ln.Close()
		if err := s.Serve(context.Background(), ln, tc); err == nil {
			t.Fatal("Serve on a closed listener returned nil")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines: %d before, %d after; Serve leaked its shutdown goroutine", before, after)
	}
}
