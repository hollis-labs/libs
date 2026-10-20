package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
// per id. status can replace the answer for a call.
type hub struct {
	mu     sync.Mutex
	ids    []string   // every id received, in order
	calls  [][]string // ids per request
	auth   []string
	n      atomic.Int32
	status func(call int32, ids []string) (code int, body string, hdr map[string]string)
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
	if h.status != nil {
		code, body, hdr := h.status(call, ids)
		if code != 0 {
			h.mu.Lock()
			h.calls = append(h.calls, ids)
			h.mu.Unlock()
			for k, v := range hdr {
				w.Header().Set(k, v)
			}
			w.WriteHeader(code)
			_, _ = io.WriteString(w, body)
			return
		}
	}
	h.mu.Lock()
	h.calls = append(h.calls, ids)
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

func TestBatchAndAuth(t *testing.T) {
	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, err := New(Config{URL: srv.URL, Token: "tok", FlushInterval: time.Hour})
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
	if st := c.Stats(); st.Sent != 3 || st.Dropped != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestPerItemResults(t *testing.T) {
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		return 200, `{"results":[{"id":"a","status":"accepted","cursor":"11"},{"id":"b","status":"duplicate","cursor":"7"},{"id":"c","status":"rejected","code":"unknown_kind","message":"nope"}],"accepted":1,"duplicate":1,"rejected":1}`, nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	var mu sync.Mutex
	var rej []string
	c, _ := New(Config{URL: srv.URL, FlushInterval: time.Hour, OnReject: func(id, code, msg string) {
		mu.Lock()
		rej = append(rej, id+":"+code)
		mu.Unlock()
	}})
	for _, id := range []string{"a", "b", "c"} {
		c.Publish(item(id, PriorityHigh))
	}
	closeClient(t, c)
	st := c.Stats()
	if st.Sent != 2 || st.Duplicates != 1 || st.Rejected != 1 {
		t.Fatalf("stats = %+v", st)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rej) != 1 || rej[0] != "c:unknown_kind" {
		t.Fatalf("rejected = %v", rej)
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
	c, _ := New(fast(Config{URL: srv.URL}))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "delivery", func() bool { return c.Stats().Sent == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.Retries < 1 || st.Dropped != 0 {
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
	c, _ := New(fast(Config{URL: srv.URL, FlushInterval: time.Hour}))
	for _, id := range []string{"a", "b", "c"} {
		c.Publish(item(id, PriorityHigh))
	}
	waitFor(t, "delivery", func() bool { return c.Stats().Sent == 3 })
	closeClient(t, c)
	if h.callIDs(0) != "a,b,c" || h.callIDs(1) != "b,c" {
		t.Fatalf("calls: %q then %q", h.callIDs(0), h.callIDs(1))
	}
}

func TestNoVerdictIsDroppedAfterMaxUnresolved(t *testing.T) {
	h := &hub{status: func(int32, []string) (int, string, map[string]string) { return 200, `{}`, nil }}
	srv := httptest.NewServer(h)
	defer srv.Close()
	var reasons sync.Map
	c, _ := New(fast(Config{URL: srv.URL, MaxUnresolved: 2, OnDrop: func(n int, r string) { reasons.Store(r, n) }}))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "drop", func() bool { return c.Stats().Dropped == 1 })
	closeClient(t, c)
	if v, ok := reasons.Load("unresolved"); !ok || v.(int) != 1 {
		t.Fatalf("drop reasons: %v", v)
	}
	if st := c.Stats(); st.Sent != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestRetryTransientThenDeliver(t *testing.T) {
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		switch call {
		case 1:
			return 503, "", nil
		case 2:
			return 429, "", map[string]string{"Retry-After": "0"}
		case 3:
			return 401, "", nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(fast(Config{URL: srv.URL}))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "delivery", func() bool { return c.Stats().Sent == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.Retries != 3 || st.Dropped != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if got := h.got(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("ids = %v (must be accepted exactly once)", got)
	}
}

func TestBadRequestDropsBatchWithoutRetry(t *testing.T) {
	h := &hub{status: func(int32, []string) (int, string, map[string]string) { return 400, "", nil }}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(fast(Config{URL: srv.URL}))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "reject", func() bool { return c.Stats().Rejected == 1 })
	closeClient(t, c)
	if h.n.Load() != 1 {
		t.Fatalf("calls = %d, want 1", h.n.Load())
	}
}

func TestTooLargeSplitsBatch(t *testing.T) {
	h := &hub{status: func(_ int32, ids []string) (int, string, map[string]string) {
		if len(ids) > 2 {
			return 413, "", nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL, FlushInterval: time.Hour})
	for i := 0; i < 5; i++ {
		c.Publish(item(fmt.Sprintf("e%d", i), PriorityHigh))
	}
	closeClient(t, c)
	if got := strings.Join(h.got(), ","); got != "e0,e1,e2,e3,e4" {
		t.Fatalf("ids = %s (order must survive splitting)", got)
	}
}

func memIDs(c *Client) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for _, q := range c.mem {
		ids = append(ids, q.ID)
	}
	return strings.Join(ids, ",")
}

func TestFullQueueEvictsLowPriorityFirst(t *testing.T) {
	var drops atomic.Int32
	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL, QueueSize: 3, BatchSize: 100, FlushInterval: time.Hour,
		OnDrop: func(n int, _ string) { drops.Add(int32(n)) }})
	c.Publish(item("low1", PriorityLow))
	c.Publish(item("high1", PriorityHigh))
	c.Publish(item("low2", PriorityLow))
	c.Publish(item("high2", PriorityHigh)) // evicts low1
	c.Publish(item("high3", PriorityHigh)) // evicts low2
	if got := memIDs(c); got != "high1,high2,high3" {
		t.Fatalf("queue = %s", got)
	}
	if drops.Load() != 2 {
		t.Fatalf("drops = %d", drops.Load())
	}
	closeClient(t, c)
	if got := strings.Join(h.got(), ","); got != "high1,high2,high3" {
		t.Fatalf("delivered = %s", got)
	}
}

// With only high-priority items held, an incoming low-priority one is the item
// that goes; an incoming high-priority one replaces the oldest.
func TestFullQueueOfHighDropsIncomingLow(t *testing.T) {
	h := &hub{}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL, QueueSize: 2, FlushInterval: time.Hour})
	c.Publish(item("h1", PriorityHigh))
	c.Publish(item("h2", PriorityHigh))
	c.Publish(item("l1", PriorityLow))
	if got := memIDs(c); got != "h1,h2" {
		t.Fatalf("queue after low = %s", got)
	}
	c.Publish(item("h3", PriorityHigh))
	if got := memIDs(c); got != "h2,h3" {
		t.Fatalf("queue after high = %s", got)
	}
	closeClient(t, c)
	if got := strings.Join(h.got(), ","); got != "h2,h3" {
		t.Fatalf("delivered = %s", got)
	}
}

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
	c, _ := New(fast(Config{URL: srv.URL, QueueSize: 4, BatchSize: 2, SpoolDir: t.TempDir()}))
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
	c, _ := New(fast(Config{URL: srv.URL, QueueSize: 1, BatchSize: 1, SpoolDir: t.TempDir()}))
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
	c1, _ := New(fast(Config{URL: dead.URL, SpoolDir: dir}))
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
	c2, _ := New(fast(Config{URL: srv.URL, SpoolDir: dir}))
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
		if _, _, err := sp.append(item(id, PriorityHigh)); err != nil {
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
	c, err := New(fast(Config{URL: srv.URL, SpoolDir: dir, Timeout: time.Hour}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("request never started")
	}
	// "Crash": read the directory as a fresh process would, without Close.
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
	c, _ := New(fast(Config{URL: srv.URL, Timeout: time.Hour}))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "request", func() bool { return true })
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
	c, _ = New(Config{URL: "http://127.0.0.1:1", QueueSize: 1, FlushInterval: time.Hour,
		OnDrop: func(int, string) {
			_ = c.Stats()
			once.Do(func() { close(called) })
		}})
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
	c, _ := New(fast(Config{URL: dead.URL}))
	c.Publish(item("a", PriorityHigh))
	closeClient(t, c)
	if st := c.Stats(); st.Dropped != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestInvalidItemsAreDropped(t *testing.T) {
	var reason atomic.Value
	c, _ := New(Config{URL: "http://127.0.0.1:1", OnDrop: func(_ int, r string) { reason.Store(r) }})
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
	c, _ := New(fast(Config{URL: srv.URL, TokenFile: f, BatchSize: 1}))
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
	p, err = FromEnv("fragments-engine", Config{FlushInterval: time.Hour})
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
	c, _ := New(Config{URL: srv.URL, FlushInterval: time.Hour})
	c.Publish(item("a", PriorityHigh))
	closeClient(t, c)
	if path.Load() != "/events" {
		t.Fatalf("path = %v", path.Load())
	}
}

func TestAppMismatchIsTerminalAndDoesNotBlockLaterEvents(t *testing.T) {
	h := &hub{status: func(call int32, ids []string) (int, string, map[string]string) {
		if ids[0] == "bad" {
			return 403, `{"error":{"code":"app_mismatch","message":"x"}}`, nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	var errs atomic.Int32
	c, _ := New(fast(Config{URL: srv.URL, BatchSize: 1, OnError: func(error) { errs.Add(1) }}))
	c.Publish(item("bad", PriorityHigh))
	c.Publish(item("good", PriorityHigh))
	waitFor(t, "good delivered", func() bool { return c.Stats().Sent == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.Rejected != 1 || st.Retries != 0 {
		t.Fatalf("stats = %+v (app_mismatch must not be retried)", st)
	}
	if errs.Load() == 0 {
		t.Fatal("app_mismatch must be reported through OnError")
	}
}

func TestOtherForbiddenAndNotYetEnabledAreRetried(t *testing.T) {
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		switch call {
		case 1:
			return 403, `{"error":{"code":"scope_required"}}`, nil
		case 2:
			return 405, "", nil
		case 3:
			return 404, "", nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(fast(Config{URL: srv.URL}))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "delivery", func() bool { return c.Stats().Sent == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.Retries != 3 || st.Dropped != 0 || st.Rejected != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// Acceptance needs a cursor, and a bare array is not the contract's answer.
func TestAcceptedWithoutCursorAndBareArrayAreUnresolved(t *testing.T) {
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		switch call {
		case 1:
			return 200, `{"results":[{"id":"a","status":"accepted"}]}`, nil
		case 2:
			return 200, `[{"id":"a","status":"accepted","cursor":"1"}]`, nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(fast(Config{URL: srv.URL}))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "delivery", func() bool { return c.Stats().Sent == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.Retries != 2 {
		t.Fatalf("stats = %+v, want 2 retries before an explicit acceptance", st)
	}
}

func TestNumericCursorIsAccepted(t *testing.T) {
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		return 200, `{"results":[{"id":"a","status":"accepted","cursor":4812}]}`, nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL, FlushInterval: time.Hour})
	c.Publish(item("a", PriorityHigh))
	closeClient(t, c)
	if st := c.Stats(); st.Sent != 1 {
		t.Fatalf("stats = %+v", st)
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
		{"rejected without a code", `{"results":[{"id":"a","status":"rejected"},{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"rejected with an unknown code", `{"results":[{"id":"a","status":"rejected","code":"nope"},{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"rejected carrying a cursor", `{"results":[{"id":"a","status":"rejected","code":"id_conflict","cursor":"9"},{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"ids out of order", `{"results":[{"id":"b","status":"accepted","cursor":"2"},{"id":"a","status":"accepted","cursor":"1"}]}`, [2]string{"", ""}},
		{"short list matched by id", `{"results":[{"id":"b","status":"accepted","cursor":"2"}]}`, [2]string{"", "accepted"}},
		{"result for a foreign id", `{"results":[{"id":"a","status":"accepted","cursor":"1"},{"id":"zzz","status":"accepted","cursor":"2"}]}`, [2]string{"accepted", ""}},
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
	// With a short list the duplicated id cannot be told apart: no verdict.
	v = verdicts(items, []byte(`{"results":[{"id":"a","status":"accepted","cursor":"1"}]}`))
	if v[0] != nil || v[1] != nil {
		t.Fatalf("ambiguous duplicate ids must be unresolved: %+v %+v", v[0], v[1])
	}
}

// By default an event without a valid verdict is never given up on.
func TestUnresolvedEventsAreRetriedUntilTheHubAnswers(t *testing.T) {
	h := &hub{status: func(call int32, _ []string) (int, string, map[string]string) {
		if call <= 12 {
			return 200, `{}`, nil
		}
		return 0, "", nil
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(fast(Config{URL: srv.URL}))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "delivery after 12 unresolved answers", func() bool { return c.Stats().Sent == 1 })
	closeClient(t, c)
	if st := c.Stats(); st.Dropped != 0 || st.Retries < 12 {
		t.Fatalf("stats = %+v", st)
	}
}

// Losing a high-priority item is the last resort and is reported as such.
func TestHighPriorityLossIsReportedSeparately(t *testing.T) {
	var mu sync.Mutex
	reasons := map[string]int{}
	c, _ := New(Config{URL: "http://127.0.0.1:1", QueueSize: 2, FlushInterval: time.Hour,
		OnDrop: func(n int, r string) { mu.Lock(); reasons[r] += n; mu.Unlock() }})
	c.Publish(item("h1", PriorityHigh))
	c.Publish(item("h2", PriorityHigh))
	c.Publish(item("l1", PriorityLow))  // the incoming low item goes
	c.Publish(item("h3", PriorityHigh)) // h1 goes
	mu.Lock()
	defer mu.Unlock()
	if reasons["queue_full"] != 1 || reasons["high_priority_dropped"] != 1 {
		t.Fatalf("reasons = %v", reasons)
	}
	if st := c.Stats(); st.Dropped != 2 || st.DroppedHigh != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestSpoolCapReportsHighPriorityLoss(t *testing.T) {
	s := mustSpool(t, t.TempDir(), 300)
	var dropped, high int
	for i := 0; i < 10; i++ {
		d, h, err := s.append(item(fmt.Sprintf("h%d", i), PriorityHigh))
		if err != nil {
			t.Fatal(err)
		}
		dropped += d
		high += h
	}
	if dropped == 0 || high != dropped {
		t.Fatalf("only high priority items were queued: dropped=%d high=%d", dropped, high)
	}
}
