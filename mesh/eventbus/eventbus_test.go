package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func item(id string, p Priority) Item {
	return Item{ID: id, Priority: p, Body: json.RawMessage(fmt.Sprintf(`{"id":%q,"kind":"x"}`, id))}
}

// hub is a test ingest endpoint. By default it accepts every event and says so
// per id, with a cursor. status can replace the answer for a call.
type hub struct {
	mu      sync.Mutex
	ids     []string   // every accepted id, in order
	calls   [][]string // ids per request
	auth    []string   // Authorization header per accepted request
	hasAuth []bool
	n       atomic.Int32
	status  func(call int32, ids []string) (code int, body string, hdr map[string]string)
}

func (h *hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	call := h.n.Add(1)
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Events []struct {
			ID string `json:"id"`
		} `json:"events"`
	}
	_ = json.Unmarshal(raw, &req)
	ids := make([]string, 0, len(req.Events))
	for _, e := range req.Events {
		ids = append(ids, e.ID)
	}
	_, present := r.Header["Authorization"]
	h.mu.Lock()
	h.calls = append(h.calls, ids)
	h.hasAuth = append(h.hasAuth, present)
	h.mu.Unlock()
	if h.status != nil {
		code, body, hdr := h.status(call, ids)
		if code != 0 {
			for k, v := range hdr {
				w.Header().Set(k, v)
			}
			w.WriteHeader(code)
			_, _ = io.WriteString(w, body)
			return
		}
	}
	h.mu.Lock()
	h.auth = append(h.auth, r.Header.Get("Authorization"))
	h.ids = append(h.ids, ids...)
	h.mu.Unlock()
	var parts []string
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf(`{"id":%q,"status":"accepted","cursor":"1"}`, id))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"results":[`+strings.Join(parts, ",")+`]}`)
}

func (h *hub) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.ids...)
}

func (h *hub) callIDs(i int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i >= len(h.calls) {
		return "<no call>"
	}
	return strings.Join(h.calls[i], ",")
}

func (h *hub) authAt(i int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i >= len(h.auth) {
		return ""
	}
	return h.auth[i]
}

func closeClient(t *testing.T, p Publisher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func fast(cfg Config) Config {
	cfg.FlushInterval = 10 * time.Millisecond
	cfg.BackoffBase = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond
	return cfg
}

func quiet(cfg Config) Config {
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return cfg
}

// memIDs lists the memory queue, oldest first.
func memIDs(c *Client) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for _, q := range c.mem {
		ids = append(ids, q.ID)
	}
	return strings.Join(ids, ",")
}

// quarantineIDs reads quarantine.jsonl in dir as "id:reason" pairs.
func quarantineIDs(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "quarantine.jsonl"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r quarantineRec
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("quarantine line %q: %v", line, err)
		}
		out = append(out, r.ID+":"+r.Reason)
	}
	return out
}

// ---- delivery and acknowledgement ----

func TestBatchAndAuth(t *testing.T) {
	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, err := New(quiet(Config{URL: srv.URL, Token: "tok", FlushInterval: time.Hour}))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		c.Publish(item(id, PriorityHigh))
	}
	closeClient(t, c)
	if got := strings.Join(h.got(), ","); got != "a,b,c" {
		t.Fatalf("ids = %s", got)
	}
	if h.authAt(0) != "Bearer tok" {
		t.Fatalf("auth = %q", h.authAt(0))
	}
	if st := c.Stats(); st.Sent != 3 || st.Dropped != 0 || st.Quarantined != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// A local relay holds the credential: with no token the request carries no
// Authorization header at all, and that is not an error.
func TestNoAuthorizationHeaderWithoutAToken(t *testing.T) {
	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	var errs atomic.Int32
	c, _ := New(quiet(Config{URL: srv.URL, FlushInterval: time.Hour, OnError: func(error) { errs.Add(1) }}))
	c.Publish(item("a", PriorityHigh))
	closeClient(t, c)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.hasAuth) != 1 || h.hasAuth[0] {
		t.Fatalf("Authorization header present = %v, want absent", h.hasAuth)
	}
	if errs.Load() != 0 || len(h.ids) != 1 {
		t.Fatalf("errors=%d delivered=%v", errs.Load(), h.ids)
	}
}

func TestPerItemResultsAndRejectedIsQuarantined(t *testing.T) {
	dir := t.TempDir()
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		return 200, `{"results":[{"id":"a","status":"accepted","cursor":"11"},{"id":"b","status":"duplicate","cursor":"7"},{"id":"c","status":"rejected","code":"unknown_kind","message":"nope"}],"accepted":1,"duplicate":1,"rejected":1}`, nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	var mu sync.Mutex
	var rej, quar []string
	c, _ := New(quiet(Config{URL: srv.URL, FlushInterval: time.Hour, SpoolDir: dir,
		OnReject: func(id, code, msg string) { mu.Lock(); rej = append(rej, id+":"+code); mu.Unlock() },
		OnQuarantine: func(it Item, reason, code string) {
			mu.Lock()
			quar = append(quar, it.ID+":"+reason+":"+code)
			mu.Unlock()
		}}))
	for _, id := range []string{"a", "b", "c"} {
		c.Publish(item(id, PriorityHigh))
	}
	closeClient(t, c)
	st := c.Stats()
	if st.Sent != 2 || st.Duplicates != 1 || st.Rejected != 1 || st.Quarantined != 1 {
		t.Fatalf("stats = %+v", st)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rej) != 1 || rej[0] != "c:unknown_kind" || len(quar) != 1 || quar[0] != "c:rejected:unknown_kind" {
		t.Fatalf("rejected=%v quarantined=%v", rej, quar)
	}
	if got := quarantineIDs(t, dir); len(got) != 1 || got[0] != "c:rejected" {
		t.Fatalf("quarantine file = %v", got)
	}
	if h.n.Load() != 1 {
		t.Fatalf("a verdict is final, calls = %d", h.n.Load())
	}
}

// A 200 that does not parse is not success: the events are retried.
func TestMalformed200IsRetriedNotCountedSent(t *testing.T) {
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		if call == 1 {
			return 200, "not json", nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL})))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "delivery", func() bool { return c.Stats().Sent == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.Retries < 1 || st.Dropped != 0 || st.Quarantined != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// Only the events without a verdict are sent again.
func TestOmittedIDsAreRetriedAlone(t *testing.T) {
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		if call == 1 {
			return 200, `{"results":[{"id":"a","status":"accepted","cursor":"1"},{"id":"c","status":"mystery"}]}`, nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL, FlushInterval: time.Hour})))
	for _, id := range []string{"a", "b", "c"} {
		c.Publish(item(id, PriorityHigh))
	}
	waitFor(t, "delivery", func() bool { return c.Stats().Sent == 3 })
	closeClient(t, c)
	if h.callIDs(0) != "a,b,c" || h.callIDs(1) != "b,c" {
		t.Fatalf("calls: %q then %q", h.callIDs(0), h.callIDs(1))
	}
}

// An event the hub never gives a valid verdict for is set aside, not dropped as
// if delivered, and the events behind it still flow.
func TestUnresolvedExhaustionIsQuarantinedAndDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	h := &hub{status: func(call int32, ids []string) (int, string, map[string]string) {
		for _, id := range ids {
			if id == "stuck" {
				// every answer that mentions "stuck" gives it no verdict
				var parts []string
				for _, o := range ids {
					if o != "stuck" {
						parts = append(parts, fmt.Sprintf(`{"id":%q,"status":"accepted","cursor":"1"}`, o))
					}
				}
				return 200, `{"results":[` + strings.Join(parts, ",") + `]}`, nil
			}
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	var reasons sync.Map
	c, _ := New(quiet(fast(Config{URL: srv.URL, MaxUnresolved: 2, SpoolDir: dir, BatchSize: 1,
		OnQuarantine: func(it Item, reason, code string) { reasons.Store(it.ID, reason) }})))
	c.Publish(item("stuck", PriorityHigh))
	c.Publish(item("behind", PriorityHigh))
	waitFor(t, "the event behind to be delivered", func() bool { return c.Stats().Sent == 1 })
	closeClient(t, c)
	if v, _ := reasons.Load("stuck"); v != "unresolved" {
		t.Fatalf("quarantine reason = %v", v)
	}
	st := c.Stats()
	if st.Quarantined != 1 || st.Sent != 1 || st.Dropped != 0 {
		t.Fatalf("stats = %+v (quarantined events are never counted as sent or dropped)", st)
	}
	if got := quarantineIDs(t, dir); len(got) != 1 || got[0] != "stuck:unresolved" {
		t.Fatalf("quarantine file = %v", got)
	}
	if got := strings.Join(h.got(), ","); got != "behind" {
		t.Fatalf("delivered = %s", got)
	}
}

func TestNegativeMaxUnresolvedRetriesForever(t *testing.T) {
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		if call <= 12 {
			return 200, `{}`, nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL, MaxUnresolved: -1})))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "delivery after 12 unresolved answers", func() bool { return c.Stats().Sent == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.Dropped != 0 || st.Quarantined != 0 || st.Retries < 12 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestTransientFailuresAreRetried(t *testing.T) {
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		switch call {
		case 1:
			return 503, "", map[string]string{"Retry-After": "0"}
		case 2:
			return 429, "", map[string]string{"Retry-After": "0"}
		case 3:
			return 405, "", nil
		case 4:
			return 404, "", nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL})))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "delivery", func() bool { return c.Stats().Sent == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.Retries != 4 || st.Dropped != 0 || st.Quarantined != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if got := h.got(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("ids = %v (must be accepted exactly once)", got)
	}
}

// Request-level failures that can never succeed set the batch aside undelivered
// and do not hold up the events behind it.
func TestFinalRequestFailuresAreQuarantined(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		reason string
		cfg    func(*Config)
	}{
		{"bad request", 400, `{"error":{"code":"bad_body"}}`, "bad_request", nil},
		{"unauthorized when strict", 401, "", "auth", func(c *Config) { c.StrictAuth = true }},
		{"forbidden scope when strict", 403, `{"error":{"code":"scope_required"}}`, "auth", func(c *Config) { c.StrictAuth = true }},
		{"app mismatch is always immediate", 403, `{"error":{"code":"app_mismatch"}}`, "app_mismatch", nil},
		{"other 403 code is final", 403, `{"error":{"code":"banned"}}`, "forbidden", nil},
		{"bare 403 is final", 403, "", "forbidden", nil},
		{"unexpected 4xx", 418, "", "http_418", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			h := &hub{status: func(call int32, ids []string) (int, string, map[string]string) {
				if ids[0] == "bad" {
					return tc.status, tc.body, nil
				}
				return 0, "", nil
			}}
			srv := httptest.NewServer(h)
			defer srv.Close()
			var errs atomic.Int32
			cfg := quiet(fast(Config{URL: srv.URL, BatchSize: 1, SpoolDir: dir, OnError: func(error) { errs.Add(1) }}))
			if tc.cfg != nil {
				tc.cfg(&cfg)
			}
			c, _ := New(cfg)
			c.Publish(item("bad", PriorityHigh))
			c.Publish(item("good", PriorityHigh))
			waitFor(t, "good delivered", func() bool { return c.Stats().Sent == 1 })
			closeClient(t, c)
			st := c.Stats()
			if st.Quarantined != 1 || st.Retries != 0 || st.Dropped != 0 {
				t.Fatalf("stats = %+v", st)
			}
			if got := quarantineIDs(t, dir); len(got) != 1 || got[0] != "bad:"+tc.reason {
				t.Fatalf("quarantine = %v, want bad:%s", got, tc.reason)
			}
			if errs.Load() == 0 {
				t.Fatal("a quarantine must be reported through OnError")
			}
		})
	}
}

// By default a 401 or a 403 scope_required keeps the events and retries.
func TestAuthFailuresAreRetriedByDefault(t *testing.T) {
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		switch call {
		case 1:
			return 401, "", nil
		case 2:
			return 403, `{"error":{"code":"scope_required"}}`, nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL})))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "delivery", func() bool { return c.Stats().Sent == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.Retries != 2 || st.Quarantined != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// A service often starts before its credential exists: the events wait, and go
// out once the token appears.
func TestServiceStartedBeforeItsCredentialDeliversWhenTheTokenAppears(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	h := &hub{status: func(call int32, ids []string) (int, string, map[string]string) {
		return 0, "", nil
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer issued" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL, TokenFile: tokenFile})))
	c.Publish(item("a", PriorityHigh))
	c.Publish(item("b", PriorityHigh))
	waitFor(t, "some auth retries", func() bool { return c.Stats().Retries >= 3 })
	if st := c.Stats(); st.Sent != 0 || st.Quarantined != 0 {
		t.Fatalf("nothing may be sent or quarantined before the credential exists: %+v", st)
	}
	if err := os.WriteFile(tokenFile, []byte("issued\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "delivery once the token exists", func() bool { return c.Stats().Sent == 2 })
	closeClient(t, c)
	if got := strings.Join(h.got(), ","); got != "a,b" {
		t.Fatalf("delivered = %s", got)
	}
	if st := c.Stats(); st.Quarantined != 0 || st.Dropped != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// Auth failures that outlast the ceiling quarantine the batch (kept, counted,
// loud), and the events behind it get their own wait.
func TestAuthCeilingQuarantinesInsteadOfWaitingForever(t *testing.T) {
	dir := t.TempDir()
	var fixed atomic.Bool
	h := &hub{status: func(call int32, ids []string) (int, string, map[string]string) {
		if !fixed.Load() {
			return 401, "", nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	var errs atomic.Int32
	c, _ := New(quiet(fast(Config{URL: srv.URL, SpoolDir: dir, BatchSize: 1, RetryAuthMax: 60 * time.Millisecond,
		OnError: func(error) { errs.Add(1) }})))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "the ceiling to quarantine the first batch", func() bool { return c.Stats().Quarantined >= 1 })
	fixed.Store(true)
	closeClient(t, c)
	if got := quarantineIDs(t, dir); len(got) == 0 || got[0] != "a:auth" {
		t.Fatalf("quarantine = %v", got)
	}
	if st := c.Stats(); st.Sent != 0 || st.Dropped != 0 {
		t.Fatalf("stats = %+v (quarantined, never sent or dropped)", st)
	}
	if errs.Load() == 0 {
		t.Fatal("the quarantine must be loud")
	}
}

func TestNegativeCeilingNeverQuarantinesAuthFailures(t *testing.T) {
	var fixed atomic.Bool
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		if !fixed.Load() {
			return 403, `{"error":{"code":"scope_required"}}`, nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL, RetryAuthMax: -1})))
	c.Publish(item("a", PriorityHigh))
	time.Sleep(150 * time.Millisecond)
	fixed.Store(true)
	waitFor(t, "delivery", func() bool { return c.Stats().Sent == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.Quarantined != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// app_mismatch is a configuration bug: immediate, even though auth retries are on.
func TestAppMismatchIsQuarantinedImmediatelyEvenWithAuthRetries(t *testing.T) {
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		return 403, `{"error":{"code":"app_mismatch"}}`, nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL})))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "quarantine", func() bool { return c.Stats().Quarantined == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.Retries != 0 || h.n.Load() != 1 {
		t.Fatalf("stats = %+v calls=%d", st, h.n.Load())
	}
}

func TestTooLargeSplitsBatchAndQuarantinesASingleEvent(t *testing.T) {
	dir := t.TempDir()
	h := &hub{status: func(_ int32, ids []string) (int, string, map[string]string) {
		for _, id := range ids {
			if id == "huge" {
				return 413, "", nil
			}
		}
		if len(ids) > 2 {
			return 413, "", nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(Config{URL: srv.URL, FlushInterval: time.Hour, SpoolDir: dir}))
	for _, id := range []string{"e0", "e1", "huge", "e3", "e4"} {
		c.Publish(item(id, PriorityHigh))
	}
	closeClient(t, c)
	if got := strings.Join(h.got(), ","); got != "e0,e1,e3,e4" {
		t.Fatalf("delivered = %s (order must survive splitting)", got)
	}
	if got := quarantineIDs(t, dir); len(got) != 1 || got[0] != "huge:payload_too_large" {
		t.Fatalf("quarantine = %v", got)
	}
}

func TestVerdictRules(t *testing.T) {
	items := []Item{{ID: "a"}, {ID: "b"}}
	cases := []struct {
		name string
		body string
		want [2]string // status of a and b, "" for no verdict
	}{
		{"both explicit", `{"results":[{"id":"a","status":"accepted","cursor":"1"},{"id":"b","status":"rejected","code":"schema_invalid"}]}`, [2]string{"accepted", "rejected"}},
		{"accepted with a code", `{"results":[{"id":"a","status":"accepted","cursor":"1","code":"x"},{"id":"b","status":"duplicate","cursor":"2"}]}`, [2]string{"", "duplicate"}},
		{"accepted without cursor", `{"results":[{"id":"a","status":"accepted"},{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"empty string cursor", `{"results":[{"id":"a","status":"accepted","cursor":""},{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"numeric cursor", `{"results":[{"id":"a","status":"accepted","cursor":4812},{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"boolean cursor", `{"results":[{"id":"a","status":"accepted","cursor":true},{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"object cursor", `{"results":[{"id":"a","status":"accepted","cursor":{"x":1}},{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"rejected without a code", `{"results":[{"id":"a","status":"rejected"},{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"rejected with an unknown code", `{"results":[{"id":"a","status":"rejected","code":"nope"},{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"rejected carrying a cursor", `{"results":[{"id":"a","status":"rejected","code":"id_conflict","cursor":"9"},{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"ids out of order", `{"results":[{"id":"b","status":"accepted","cursor":"2"},{"id":"a","status":"accepted","cursor":"1"}]}`, [2]string{"", ""}},
		{"short list matched by id", `{"results":[{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"result for a foreign id", `{"results":[{"id":"a","status":"accepted","cursor":"1"},{"id":"zzz","status":"accepted","cursor":"2"}]}`, [2]string{"accepted", ""}},
		{"unknown status", `{"results":[{"id":"a","status":"mystery","cursor":"1"},{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"bare array", `[{"id":"a","status":"accepted","cursor":"1"},{"id":"b","status":"accepted","cursor":"2"}]`, [2]string{"", ""}},
		{"not json", `oops`, [2]string{"", ""}},
	}
	for _, c := range cases {
		got := verdicts(items, []byte(c.body))
		for k := range items {
			status := ""
			if got[k] != nil {
				status = got[k].Status
			}
			if status != c.want[k] {
				t.Errorf("%s: item %s = %q, want %q", c.name, items[k].ID, status, c.want[k])
			}
		}
	}
}

// The same id twice in one request is answered by position.
func TestDuplicateIDsInOneBatchAreMatchedByPosition(t *testing.T) {
	items := []Item{{ID: "a"}, {ID: "a"}}
	v := verdicts(items, []byte(`{"results":[{"id":"a","status":"accepted","cursor":"1"},{"id":"a","status":"duplicate","cursor":"1"}]}`))
	if v[0] == nil || v[0].Status != "accepted" || v[1] == nil || v[1].Status != "duplicate" {
		t.Fatalf("verdicts = %+v %+v", v[0], v[1])
	}
	v = verdicts(items, []byte(`{"results":[{"id":"a","status":"accepted","cursor":"1"}]}`))
	if v[0] != nil || v[1] != nil {
		t.Fatalf("ambiguous duplicate ids must be unresolved: %+v %+v", v[0], v[1])
	}
}

func TestIngestURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://hub:7777":         "http://hub:7777/events",
		"http://hub:7777/":        "http://hub:7777/events",
		"http://hub:7777/events":  "http://hub:7777/events",
		"http://hub:7777/events/": "http://hub:7777/events",
		"http://hub/api":          "http://hub/api/events",
		"http://hub:7777?x=1":     "http://hub:7777/events?x=1",
		"  http://hub:7777  ":     "http://hub:7777/events",
	} {
		if got := ingestURL(in); got != want {
			t.Errorf("ingestURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPostsToEventsPathOfBaseURL(t *testing.T) {
	var path atomic.Value
	h := &hub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.Path)
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c, _ := New(quiet(Config{URL: srv.URL, FlushInterval: time.Hour}))
	c.Publish(item("a", PriorityHigh))
	closeClient(t, c)
	if path.Load() != "/events" {
		t.Fatalf("path = %v", path.Load())
	}
}

// ---- overflow, priority and loss ----

func TestFullQueueEvictsDebugThenActivityBeforeProtected(t *testing.T) {
	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	var drops atomic.Int32
	c, _ := New(quiet(Config{URL: srv.URL, QueueSize: 4, BatchSize: 100, FlushInterval: time.Hour,
		OnDrop: func(n int, _ string) { drops.Add(int32(n)) }}))
	c.Publish(item("act1", PriorityActivity))
	c.Publish(item("dbg1", PriorityDebug))
	c.Publish(item("high1", PriorityHigh))
	c.Publish(item("act2", PriorityActivity))
	c.Publish(item("high2", PriorityHigh)) // evicts dbg1 first
	if got := memIDs(c); got != "act1,high1,act2,high2" {
		t.Fatalf("queue = %s", got)
	}
	c.Publish(item("high3", PriorityHigh)) // then the oldest activity
	if got := memIDs(c); got != "high1,act2,high2,high3" {
		t.Fatalf("queue = %s", got)
	}
	c.Publish(item("high4", PriorityHigh)) // then the other activity
	if got := memIDs(c); got != "high1,high2,high3,high4" {
		t.Fatalf("queue = %s", got)
	}
	if st := c.Stats(); st.DroppedHigh != 0 || len(st.Losses) != 0 {
		t.Fatalf("no protected item may be lost yet: %+v", st)
	}
	closeClient(t, c)
	if got := strings.Join(h.got(), ","); got != "high1,high2,high3,high4" {
		t.Fatalf("delivered = %s", got)
	}
}

func TestIncomingLessImportantThanEverythingHeldIsRefused(t *testing.T) {
	c, _ := New(quiet(Config{URL: "http://127.0.0.1:1", QueueSize: 2, FlushInterval: time.Hour}))
	c.Publish(item("act1", PriorityActivity))
	c.Publish(item("high1", PriorityHigh))
	c.Publish(item("dbg1", PriorityDebug)) // below everything held: refused
	if got := memIDs(c); got != "act1,high1" {
		t.Fatalf("queue = %s", got)
	}
	c.Publish(item("high2", PriorityHigh)) // evicts the activity item, not a protected one
	if got := memIDs(c); got != "high1,high2" {
		t.Fatalf("queue = %s", got)
	}
	c.Publish(item("act2", PriorityActivity)) // below everything held: refused before any protected loss
	if got := memIDs(c); got != "high1,high2" {
		t.Fatalf("queue = %s", got)
	}
	if st := c.Stats(); st.DroppedHigh != 0 || len(st.Losses) != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

type logCapture struct {
	mu   sync.Mutex
	warn []string
}

func (l *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (l *logCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		l.mu.Lock()
		l.warn = append(l.warn, r.Message)
		l.mu.Unlock()
	}
	return nil
}
func (l *logCapture) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logCapture) WithGroup(string) slog.Handler      { return l }

// Losing a protected item is the last resort: it is counted, logged at WARN,
// reported with its own reason, and recorded with class, times and count.
func TestProtectedLossIsRecordedLoggedAndReported(t *testing.T) {
	var mu sync.Mutex
	reasons := map[string]int{}
	lc := &logCapture{}
	c, _ := New(Config{URL: "http://127.0.0.1:1", QueueSize: 2, FlushInterval: time.Hour, Logger: slog.New(lc),
		OnDrop: func(n int, r string) { mu.Lock(); reasons[r] += n; mu.Unlock() }})
	before := time.Now().UTC().Add(-time.Second)
	c.Publish(item("h1", PriorityHigh))
	c.Publish(item("h2", PriorityHigh))
	c.Publish(item("h3", PriorityHigh)) // h1 is lost
	c.Publish(item("h4", PriorityHigh)) // h2 is lost
	if got := memIDs(c); got != "h3,h4" {
		t.Fatalf("queue = %s", got)
	}
	mu.Lock()
	if reasons["high_priority_dropped"] != 2 {
		t.Fatalf("reasons = %v", reasons)
	}
	mu.Unlock()
	st := c.Stats()
	if st.DroppedHigh != 2 || st.Dropped != 2 {
		t.Fatalf("stats = %+v", st)
	}
	if len(st.Losses) != 1 {
		t.Fatalf("losses = %+v", st.Losses)
	}
	l := st.Losses[0]
	if l.Class != "protected" || l.Count != 2 || l.First.Before(before) || l.Last.Before(l.First) {
		t.Fatalf("loss record = %+v", l)
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if len(lc.warn) == 0 || !strings.Contains(lc.warn[0], "protected events dropped") {
		t.Fatalf("WARN log = %v", lc.warn)
	}
}

// The loss record survives a restart and is announced by a gap marker, built by
// the caller, the next time the hub accepts events; then it is cleared.
func TestLossRecordPersistsAndGapMarkerIsAnnounced(t *testing.T) {
	dir := t.TempDir()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	c1, _ := New(quiet(fast(Config{URL: dead.URL, SpoolDir: dir, SpoolMaxBytes: 700, QueueSize: 1, BatchSize: 1})))
	for i := 0; i < 20; i++ {
		c1.Publish(item(fmt.Sprintf("h%02d", i), PriorityHigh))
	}
	st := c1.Stats()
	if st.DroppedHigh == 0 || len(st.Losses) != 1 || st.Losses[0].Count != st.DroppedHigh {
		t.Fatalf("loss not recorded: %+v", st)
	}
	closeClient(t, c1) // closing writes the leftovers to the spool, which may cost more
	dead.Close()
	st = c1.Stats()
	if len(st.Losses) != 1 || st.Losses[0].Count != st.DroppedHigh {
		t.Fatalf("loss record and counter disagree after close: %+v", st)
	}
	lost := st.Losses[0].Count

	// A new process on the same directory still knows about the loss.
	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	var marks atomic.Int32
	var seen atomic.Value
	c2, _ := New(quiet(fast(Config{URL: srv.URL, SpoolDir: dir, SpoolMaxBytes: 700,
		GapMarker: func(l Loss) (Item, error) {
			marks.Add(1)
			seen.Store(l)
			return Item{ID: "gap1", Priority: PriorityHigh, Body: json.RawMessage(`{"id":"gap1","kind":"alert.raised"}`)}, nil
		}})))
	if got := c2.Stats().Losses; len(got) != 1 || got[0].Count != lost {
		t.Fatalf("loss record after restart = %+v, want count %d", got, lost)
	}
	waitFor(t, "gap marker delivered", func() bool {
		for _, id := range h.got() {
			if id == "gap1" {
				return true
			}
		}
		return false
	})
	closeClient(t, c2)
	if marks.Load() != 1 {
		t.Fatalf("gap marker built %d times", marks.Load())
	}
	if l := seen.Load().(Loss); l.Class != "protected" || l.Count != lost {
		t.Fatalf("marker was given %+v", l)
	}
	if got := c2.Stats().Losses; len(got) != 0 {
		t.Fatalf("record not cleared after the marker: %+v", got)
	}
	if got := openLossBook(dir).list(); len(got) != 0 {
		t.Fatalf("record still on disk: %+v", got)
	}
}

func TestQuarantineIsBoundedOldestFirst(t *testing.T) {
	dir := t.TempDir()
	h := &hub{status: func(_ int32, ids []string) (int, string, map[string]string) {
		var parts []string
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf(`{"id":%q,"status":"rejected","code":"unknown_kind"}`, id))
		}
		return 200, `{"results":[` + strings.Join(parts, ",") + `]}`, nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(Config{URL: srv.URL, FlushInterval: time.Hour, SpoolDir: dir, QuarantineMaxBytes: 1200, BatchSize: 1}))
	for i := 0; i < 30; i++ {
		c.Publish(item(fmt.Sprintf("r%02d", i), PriorityHigh))
	}
	closeClient(t, c)
	got := quarantineIDs(t, dir)
	if len(got) == 0 || len(got) >= 30 {
		t.Fatalf("quarantine holds %d records, want some but not all", len(got))
	}
	if got[len(got)-1] != "r29:rejected" {
		t.Fatalf("newest record missing: %v", got)
	}
	if st, _ := os.Stat(filepath.Join(dir, "quarantine.jsonl")); st.Size() > 1200 {
		t.Fatalf("quarantine file is %d bytes, cap 1200", st.Size())
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("not oldest-first: %v", got)
		}
	}
	if c.Stats().Quarantined != 30 {
		t.Fatalf("every rejected event counts as quarantined: %+v", c.Stats())
	}
}

// ---- spool behaviour through the client ----

func TestSpoolOverflowKeepsOrder(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	h := &hub{status: func(int32, []string) (int, string, map[string]string) {
		if down.Load() {
			return 503, "", nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL, QueueSize: 4, BatchSize: 2, SpoolDir: t.TempDir()})))
	var want []string
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("e%02d", i)
		want = append(want, id)
		c.Publish(item(id, PriorityHigh))
	}
	waitFor(t, "spooling", func() bool { return c.Stats().SpoolBytes > 0 })
	down.Store(false)
	waitFor(t, "drain", func() bool { return c.Stats().Sent == 20 })
	closeClient(t, c)
	if got := strings.Join(h.got(), ","); got != strings.Join(want, ",") {
		t.Fatalf("order lost:\n got %s\nwant %s", got, strings.Join(want, ","))
	}
	if st := c.Stats(); st.Dropped != 0 || st.SpoolBytes != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// QueueSize 1 must still drain the spool.
func TestQueueSizeOneDeliversOverflow(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	h := &hub{status: func(int32, []string) (int, string, map[string]string) {
		if down.Load() {
			return 503, "", nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL, QueueSize: 1, BatchSize: 1, SpoolDir: t.TempDir()})))
	for i := 0; i < 6; i++ {
		c.Publish(item(fmt.Sprintf("q%d", i), PriorityHigh))
	}
	down.Store(false)
	waitFor(t, "drain", func() bool { return c.Stats().Sent == 6 })
	closeClient(t, c)
	if got := strings.Join(h.got(), ","); got != "q0,q1,q2,q3,q4,q5" {
		t.Fatalf("delivered = %s", got)
	}
}

func TestCloseKeepsUnsentInSpoolAndNextClientResumes(t *testing.T) {
	dir := t.TempDir()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	c1, _ := New(quiet(fast(Config{URL: dead.URL, SpoolDir: dir})))
	for _, id := range []string{"a", "b", "c"} {
		c1.Publish(item(id, PriorityHigh))
	}
	closeClient(t, c1)
	dead.Close()
	if st := c1.Stats(); st.Dropped != 0 {
		t.Fatalf("nothing may be dropped with a spool, stats = %+v", st)
	}

	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c2, _ := New(quiet(fast(Config{URL: srv.URL, SpoolDir: dir})))
	c2.Publish(item("d", PriorityHigh))
	waitFor(t, "resume", func() bool { return c2.Stats().Sent == 4 })
	closeClient(t, c2)
	if got := strings.Join(h.got(), ","); got != "a,b,c,d" {
		t.Fatalf("ids = %s", got)
	}
}

// Spooled events stay on disk until the hub answers: a crash while a request is
// in flight loses nothing.
func TestInFlightSpoolRecordsSurviveACrash(t *testing.T) {
	dir := t.TempDir()
	sp, err := openSpool(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := sp.append(item(id, PriorityHigh)); err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	}))
	defer srv.Close()
	defer close(release)
	c, err := New(quiet(fast(Config{URL: srv.URL, SpoolDir: dir, Timeout: time.Hour})))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("request never started")
	}
	again, err := openSpool(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.peek(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "a" || got[2].ID != "c" {
		t.Fatalf("after crash: %+v", got)
	}
	_ = c
}

func TestSecondCloseHonoursItsOwnContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c, _ := New(quiet(fast(Config{URL: srv.URL, Timeout: time.Hour})))
	c.Publish(item("a", PriorityHigh))
	time.Sleep(50 * time.Millisecond)

	ctx1, cancel1 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel1()
	if err := c.Close(ctx1); err == nil {
		t.Fatal("first Close should time out while the request is blocked")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	start := time.Now()
	if err := c.Close(ctx2); err == nil {
		t.Fatal("second Close with a canceled context must return its error")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("second Close ignored its context for %v", time.Since(start))
	}
}

// OnDrop may call back into the client without deadlocking.
func TestCallbacksRunOutsideTheLock(t *testing.T) {
	var c *Client
	var once sync.Once
	called := make(chan struct{})
	c, _ = New(quiet(Config{URL: "http://127.0.0.1:1", QueueSize: 1, FlushInterval: time.Hour,
		OnDrop: func(int, string) {
			_ = c.Stats()
			once.Do(func() { close(called) })
		}}))
	c.Publish(item("a", PriorityHigh))
	c.Publish(item("b", PriorityHigh))
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("OnDrop blocked: callback ran under the lock")
	}
}

func TestCloseWithoutSpoolCountsDrop(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer dead.Close()
	c, _ := New(quiet(fast(Config{URL: dead.URL})))
	c.Publish(item("a", PriorityActivity))
	closeClient(t, c)
	if st := c.Stats(); st.Dropped != 1 || st.DroppedHigh != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestInvalidItemsAreDropped(t *testing.T) {
	var reason atomic.Value
	c, _ := New(quiet(Config{URL: "http://127.0.0.1:1", OnDrop: func(_ int, r string) { reason.Store(r) }}))
	c.Publish(Item{ID: "x", Body: json.RawMessage(`{not json`)})
	if reason.Load() != "invalid" {
		t.Fatalf("bad json: reason = %v", reason.Load())
	}
	reason.Store("")
	c.Publish(Item{Body: json.RawMessage(`{}`)})
	if reason.Load() != "invalid" {
		t.Fatalf("empty id: reason = %v", reason.Load())
	}
	closeClient(t, c)
}

func TestTokenFileIsReread(t *testing.T) {
	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	f := filepath.Join(t.TempDir(), "tok")
	_ = os.WriteFile(f, []byte("one\n"), 0o600)
	c, _ := New(quiet(fast(Config{URL: srv.URL, TokenFile: f, BatchSize: 1})))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "first", func() bool { return c.Stats().Sent == 1 })
	_ = os.WriteFile(f, []byte("two\n"), 0o600)
	c.Publish(item("b", PriorityHigh))
	waitFor(t, "second", func() bool { return c.Stats().Sent == 2 })
	closeClient(t, c)
	if h.authAt(0) != "Bearer one" || h.authAt(1) != "Bearer two" {
		t.Fatalf("auth = %q %q", h.authAt(0), h.authAt(1))
	}
}

func TestNewRequiresURL(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("want error")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvURL, "")
	p, err := FromEnv("cerberus", Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(Noop); !ok {
		t.Fatalf("got %T, want Noop", p)
	}
	p.Publish(item("a", PriorityHigh))

	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	f := filepath.Join(t.TempDir(), "tok")
	_ = os.WriteFile(f, []byte("filetok"), 0o600)
	t.Setenv(EnvURL, srv.URL)
	t.Setenv(TokenFileEnv("fragments-engine"), f)
	p, err = FromEnv("fragments-engine", quiet(Config{FlushInterval: time.Hour}))
	if err != nil {
		t.Fatal(err)
	}
	p.Publish(item("a", PriorityHigh))
	closeClient(t, p)
	if h.authAt(0) != "Bearer filetok" {
		t.Fatalf("auth = %q", h.authAt(0))
	}
}

func TestTokenFileEnvName(t *testing.T) {
	if got := TokenFileEnv("fragments-engine"); got != "HOLLIS_AUTH_TOKEN_FILE_FRAGMENTS_ENGINE" {
		t.Fatalf("got %s", got)
	}
}

// ---- gap marker acknowledgement and failure paths ----

// lossDir returns a spool directory holding a persisted protected-loss record,
// as left by a process that overflowed while the hub was down.
func lossDir(t *testing.T) (dir string, lost uint64) {
	t.Helper()
	dir = t.TempDir()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer dead.Close()
	c, _ := New(quiet(fast(Config{URL: dead.URL, SpoolDir: dir, SpoolMaxBytes: 700, QueueSize: 1, BatchSize: 1})))
	for i := 0; i < 20; i++ {
		c.Publish(item(fmt.Sprintf("h%02d", i), PriorityHigh))
	}
	closeClient(t, c)
	st := c.Stats()
	if len(st.Losses) != 1 {
		t.Fatalf("setup: no loss recorded: %+v", st)
	}
	return dir, st.Losses[0].Count
}

func gapItem() (Item, error) {
	return Item{ID: "gap1", Priority: PriorityHigh, Body: json.RawMessage(`{"id":"gap1","kind":"alert.raised"}`)}, nil
}

// The record is cleared only when the hub accepts the marker. A marker the hub
// rejects leaves the loss on record, and is not announced again in a loop.
func TestRejectedGapMarkerLeavesTheLossOnRecord(t *testing.T) {
	dir, lost := lossDir(t)
	h := &hub{status: func(_ int32, ids []string) (int, string, map[string]string) {
		for _, id := range ids {
			if id == "gap1" {
				return 200, `{"results":[{"id":"gap1","status":"rejected","code":"unknown_kind"}]}`, nil
			}
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	var marks atomic.Int32
	c, _ := New(quiet(fast(Config{URL: srv.URL, SpoolDir: dir, SpoolMaxBytes: 700, GapMarker: func(Loss) (Item, error) {
		marks.Add(1)
		return gapItem()
	}})))
	waitFor(t, "the marker to be rejected and quarantined", func() bool { return c.Stats().Quarantined >= 1 })
	time.Sleep(150 * time.Millisecond) // a loop would announce again
	closeClient(t, c)
	if marks.Load() != 1 {
		t.Fatalf("marker built %d times, want once", marks.Load())
	}
	got := c.Stats().Losses
	if len(got) != 1 || got[0].Count != lost {
		t.Fatalf("the loss must stay on record when the marker was not accepted: %+v (want count %d)", got, lost)
	}
}

// Losses that happen while a marker is in flight are not erased by its
// acknowledgement: only the announced items come off the record.
func TestLossesDuringAMarkerSurviveItsAcknowledgement(t *testing.T) {
	dir, lost := lossDir(t)
	inFlight := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h := &hub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(body), `"gap1"`):
			once.Do(func() { close(inFlight) })
			<-release
		case strings.Contains(string(body), `"gap2"`):
			// The marker for the later losses is rejected, so what it announced
			// stays visible on the record for the assertion below.
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"results":[{"id":"gap2","status":"rejected","code":"unknown_kind"}]}`)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	var n atomic.Int32
	c, _ := New(quiet(fast(Config{URL: srv.URL, SpoolDir: dir, SpoolMaxBytes: 700, GapMarker: func(Loss) (Item, error) {
		id := fmt.Sprintf("gap%d", n.Add(1))
		return Item{ID: id, Priority: PriorityHigh, Body: json.RawMessage(fmt.Sprintf(`{"id":%q,"kind":"alert.raised"}`, id))}, nil
	}})))
	select {
	case <-inFlight:
	case <-time.After(10 * time.Second):
		t.Fatal("the marker never reached the hub")
	}
	// The record is still on disk while the marker is unacknowledged.
	raw, err := os.ReadFile(filepath.Join(dir, "loss.json"))
	if err != nil || !strings.Contains(string(raw), `"marker_id":"gap1"`) {
		t.Fatalf("loss.json while in flight: %s (%v)", raw, err)
	}
	// Two more protected items are lost meanwhile.
	c.mu.Lock()
	c.recordLoss(2)
	c.mu.Unlock()
	close(release)
	waitFor(t, "the second marker to be rejected", func() bool { return c.Stats().Quarantined >= 1 })
	closeClient(t, c)
	got := c.Stats().Losses
	if len(got) != 1 || got[0].Count != 2 {
		t.Fatalf("after the first marker's acknowledgement only the later losses remain: %+v (the first marker announced %d)", got, lost)
	}
}

// A marker that never reached the hub (crash, eviction) is announced again by
// the next process, and the record is cleared only by an accepted marker.
func TestUnacknowledgedMarkerIsAnnouncedAgainAfterARestart(t *testing.T) {
	dir, lost := lossDir(t)
	stuck := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"gap1"`) {
			<-r.Context().Done() // never answers: the process "crashes" here
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		(&hub{}).ServeHTTP(w, r)
	}))
	var marks atomic.Int32
	mk := func(Loss) (Item, error) { marks.Add(1); return gapItem() }
	c1, _ := New(quiet(fast(Config{URL: stuck.URL, SpoolDir: dir, SpoolMaxBytes: 700, GapMarker: mk, Timeout: time.Hour})))
	waitFor(t, "marker in flight", func() bool {
		raw, _ := os.ReadFile(filepath.Join(dir, "loss.json"))
		return strings.Contains(string(raw), `"marker_id":"gap1"`)
	})
	_ = c1
	stuck.CloseClientConnections()

	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c2, _ := New(quiet(fast(Config{URL: srv.URL, SpoolDir: dir, SpoolMaxBytes: 700, GapMarker: mk})))
	waitFor(t, "record cleared by an accepted marker", func() bool { return len(c2.Stats().Losses) == 0 })
	closeClient(t, c2)
	if marks.Load() < 2 {
		t.Fatalf("marker built %d times; the new process must announce again", marks.Load())
	}
	if got := openLossBook(dir).list(); len(got) != 0 {
		t.Fatalf("loss still on disk (was %d): %+v", lost, got)
	}
}

// If the quarantine cannot be written, the event is not settled: it stays in the
// spool, is not counted as quarantined, and is quarantined once storage works.
func TestQuarantineWriteFailureKeepsTheEvent(t *testing.T) {
	dir := t.TempDir()
	sp, err := openSpool(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sp.append(item("bad", PriorityHigh)); err != nil {
		t.Fatal(err)
	}
	// A directory where the quarantine file should be.
	if err := os.Mkdir(filepath.Join(dir, "quarantine.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := &hub{status: func(int32, []string) (int, string, map[string]string) { return 400, "", nil }}
	srv := httptest.NewServer(h)
	defer srv.Close()
	var errs atomic.Int32
	c, err := New(quiet(fast(Config{URL: srv.URL, SpoolDir: dir, OnError: func(error) { errs.Add(1) }})))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a reported storage failure", func() bool { return errs.Load() >= 2 })
	st := c.Stats()
	if st.Quarantined != 0 || st.SpoolBytes == 0 {
		t.Fatalf("the event must stay in the spool and not be counted as quarantined: %+v", st)
	}
	if err := os.Remove(filepath.Join(dir, "quarantine.jsonl")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "quarantine once storage works", func() bool { return c.Stats().Quarantined == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.SpoolBytes != 0 || st.Sent != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if got := quarantineIDs(t, dir); len(got) != 1 || got[0] != "bad:bad_request" {
		t.Fatalf("quarantine = %v", got)
	}
}

// OnError may call back into the client even when the loss record cannot be
// saved: the error is reported outside the lock.
func TestLossSaveFailureIsReportedOutsideTheLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "loss.json.tmp"), 0o700); err != nil { // makes every save fail
		t.Fatal(err)
	}
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer dead.Close()
	var c *Client
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, _ = New(quiet(fast(Config{URL: dead.URL, SpoolDir: dir, SpoolMaxBytes: 300, QueueSize: 1, BatchSize: 1,
			OnError: func(error) { calls.Add(1); _ = c.Stats() }})))
		for i := 0; i < 20; i++ {
			c.Publish(item(fmt.Sprintf("h%02d", i), PriorityHigh))
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: OnError ran under the lock")
	}
	if calls.Load() == 0 {
		t.Fatal("the save failure was never reported")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Close(ctx)
}

// ---- Close races ----

func spoolIDs(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	sp, err := openSpool(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	items, err := sp.peek(100000)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range items {
		out[q.ID] = true
	}
	return out
}

// Publish racing with Close: every event is either on disk for the next
// process or reported as dropped because the client was closed. None vanishes.
func TestPublishRacingCloseAccountsForEveryEvent(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer dead.Close()
	for round := 0; round < 15; round++ {
		dir := t.TempDir()
		var closedDrops atomic.Int64
		c, err := New(quiet(fast(Config{URL: dead.URL, SpoolDir: dir, QueueSize: 100000,
			OnDrop: func(n int, reason string) {
				if reason == "closed" {
					closedDrops.Add(int64(n))
				}
			}})))
		if err != nil {
			t.Fatal(err)
		}
		const writers, each = 4, 25
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < each; i++ {
					c.Publish(item(fmt.Sprintf("r%d-w%d-%02d", round, w, i), PriorityHigh))
				}
			}(w)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(round%5) * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = c.Close(ctx)
		}()
		wg.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := c.Close(ctx); err != nil {
			t.Fatalf("round %d: close: %v", round, err)
		}
		cancel()
		onDisk := len(spoolIDs(t, dir))
		if total := onDisk + int(closedDrops.Load()); total != writers*each {
			t.Fatalf("round %d: %d on disk + %d reported closed = %d, want %d: events vanished", round, onDisk, closedDrops.Load(), total, writers*each)
		}
	}
}

// Quarantining while Close runs: an event is in the quarantine file or still
// pending (spooled), never neither.
func TestQuarantineRacingCloseNeverLosesAnEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	defer srv.Close()
	for round := 0; round < 15; round++ {
		dir := t.TempDir()
		c, err := New(quiet(fast(Config{URL: srv.URL, SpoolDir: dir, BatchSize: 1})))
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{"a", "b", "c", "d", "e", "f"}
		for _, id := range ids {
			c.Publish(item(id, PriorityHigh))
		}
		time.Sleep(time.Duration(round%4) * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := c.Close(ctx); err != nil {
			t.Fatalf("round %d: close: %v", round, err)
		}
		cancel()
		held := spoolIDs(t, dir)
		for _, id := range quarantineIDs(t, dir) {
			held[strings.SplitN(id, ":", 2)[0]] = true
		}
		for _, id := range ids {
			if !held[id] {
				t.Fatalf("round %d: event %s is neither pending nor quarantined: %v", round, id, held)
			}
		}
		if st := c.Stats(); st.Dropped != 0 {
			t.Fatalf("round %d: stats = %+v", round, st)
		}
	}
}

// ---- Deliver (synchronous delivery for an outbox relay) ----

func dispo(ds []Disposition) string {
	var out []string
	for _, d := range ds {
		out = append(out, map[Disposition]string{DispositionPending: "pending", DispositionSent: "sent", DispositionQuarantined: "quarantined"}[d])
	}
	return strings.Join(out, ",")
}

func TestDeliverReportsEachItemsDisposition(t *testing.T) {
	dir := t.TempDir()
	h := &hub{status: func(call int32, ids []string) (int, string, map[string]string) {
		return 200, `{"results":[{"id":"a","status":"accepted","cursor":"1"},{"id":"b","status":"duplicate","cursor":"2"},{"id":"c","status":"rejected","code":"unknown_kind"}]}`, nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(Config{URL: srv.URL, SpoolDir: dir, FlushInterval: time.Hour}))
	defer closeClient(t, c)
	got := c.Deliver(context.Background(), []Item{item("a", PriorityHigh), item("b", PriorityHigh), item("c", PriorityHigh)})
	if dispo(got) != "sent,sent,quarantined" {
		t.Fatalf("dispositions = %s", dispo(got))
	}
	if q := quarantineIDs(t, dir); len(q) != 1 || q[0] != "c:rejected" {
		t.Fatalf("quarantine = %v (a quarantined item must be durable before Deliver reports it)", q)
	}
	if st := c.Stats(); st.Queued != 0 || st.Sent != 2 {
		t.Fatalf("Deliver must bypass the queue: %+v", st)
	}
}

func TestDeliverRetriesTransientFailuresAndSplitsBatches(t *testing.T) {
	h := &hub{status: func(call int32, ids []string) (int, string, map[string]string) {
		if call == 1 {
			return 503, "", nil
		}
		if len(ids) > 2 {
			return 413, "", nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL, FlushInterval: time.Hour})))
	defer closeClient(t, c)
	items := []Item{item("a", PriorityHigh), item("b", PriorityHigh), item("c", PriorityHigh), item("d", PriorityHigh)}
	got := c.Deliver(context.Background(), items)
	if dispo(got) != "sent,sent,sent,sent" {
		t.Fatalf("dispositions = %s", dispo(got))
	}
	if ids := strings.Join(h.got(), ","); ids != "a,b,c,d" {
		t.Fatalf("delivered = %s, want the original order", ids)
	}
}

// A request that can never succeed quarantines its items and the next chunk
// still goes out.
func TestDeliverQuarantinesFinalFailuresAndContinues(t *testing.T) {
	dir := t.TempDir()
	h := &hub{status: func(call int32, ids []string) (int, string, map[string]string) {
		if ids[0] == "bad" {
			return 400, "", nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL, SpoolDir: dir, BatchSize: 1})))
	defer closeClient(t, c)
	got := c.Deliver(context.Background(), []Item{item("bad", PriorityHigh), item("good", PriorityHigh)})
	if dispo(got) != "quarantined,sent" {
		t.Fatalf("dispositions = %s", dispo(got))
	}
	if q := quarantineIDs(t, dir); len(q) != 1 || q[0] != "bad:bad_request" {
		t.Fatalf("quarantine = %v", q)
	}
}

// When the call ends before everything is settled, the settled prefix stays
// settled and the rest is Pending; nothing is recorded for Pending items.
func TestDeliverLeavesTheUnsettledPending(t *testing.T) {
	dir := t.TempDir()
	block := make(chan struct{})
	h := &hub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"second"`) {
			select {
			case <-block:
			case <-r.Context().Done():
			}
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	defer close(block)
	c, _ := New(quiet(fast(Config{URL: srv.URL, SpoolDir: dir, BatchSize: 1, Timeout: time.Hour})))
	defer closeClient(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	got := c.Deliver(ctx, []Item{item("first", PriorityHigh), item("second", PriorityHigh), item("third", PriorityHigh)})
	if dispo(got) != "sent,pending,pending" {
		t.Fatalf("dispositions = %s", dispo(got))
	}
	if q := quarantineIDs(t, dir); len(q) != 0 {
		t.Fatalf("pending items must not be recorded anywhere: %v", q)
	}
}

func TestDeliverWithTheHubDownEndsWithTheContext(t *testing.T) {
	c, _ := New(quiet(fast(Config{URL: deadHub(t).URL})))
	defer closeClient(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := c.Deliver(ctx, []Item{item("a", PriorityHigh)})
	if dispo(got) != "pending" || time.Since(start) > 5*time.Second {
		t.Fatalf("dispositions = %s after %v", dispo(got), time.Since(start))
	}
}

func TestDeliverQuarantinesUnsendableItems(t *testing.T) {
	dir := t.TempDir()
	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL, SpoolDir: dir})))
	defer closeClient(t, c)
	got := c.Deliver(context.Background(), []Item{
		item("ok1", PriorityHigh),
		{ID: "", Body: json.RawMessage(`{}`)},
		{ID: "badjson", Body: json.RawMessage(`{nope`)},
		item("ok2", PriorityHigh),
	})
	if dispo(got) != "sent,quarantined,quarantined,sent" {
		t.Fatalf("dispositions = %s", dispo(got))
	}
}

func TestDeliverOnAClosedClientSettlesNothing(t *testing.T) {
	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL})))
	closeClient(t, c)
	got := c.Deliver(context.Background(), []Item{item("a", PriorityHigh)})
	if dispo(got) == "sent" {
		// A closed client may still complete one request that wins the race; it
		// must never claim more than it did.
		if len(h.got()) != 1 {
			t.Fatalf("reported sent but the hub saw %v", h.got())
		}
		return
	}
	if dispo(got) != "pending" {
		t.Fatalf("dispositions = %s", dispo(got))
	}
}

// Deliver and the queue sender run side by side without sharing retry state.
func TestDeliverAndTheQueueRunConcurrently(t *testing.T) {
	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(quiet(fast(Config{URL: srv.URL})))
	var wg sync.WaitGroup
	wg.Add(2)
	var got []Disposition
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			c.Publish(item(fmt.Sprintf("q%02d", i), PriorityHigh))
		}
	}()
	go func() {
		defer wg.Done()
		var items []Item
		for i := 0; i < 40; i++ {
			items = append(items, item(fmt.Sprintf("d%02d", i), PriorityHigh))
		}
		got = c.Deliver(context.Background(), items)
	}()
	wg.Wait()
	waitFor(t, "queued events delivered", func() bool { return c.Stats().Sent == 80 })
	closeClient(t, c)
	for i, d := range got {
		if d != DispositionSent {
			t.Fatalf("item %d: %v", i, d)
		}
	}
}

func deadHub(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	t.Cleanup(s.Close)
	return s
}
