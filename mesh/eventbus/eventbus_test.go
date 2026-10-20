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

// hub is a test ingest endpoint.
type hub struct {
	mu     sync.Mutex
	ids    []string
	auth   []string
	calls  atomic.Int32
	status func(call int32, n int) (code int, body string, hdr map[string]string)
}

func (h *hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	call := h.calls.Add(1)
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Events []struct {
			ID string `json:"id"`
		} `json:"events"`
	}
	_ = json.Unmarshal(raw, &req)
	if h.status != nil {
		code, body, hdr := h.status(call, len(req.Events))
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
	for _, e := range req.Events {
		h.ids = append(h.ids, e.ID)
	}
	h.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"results":[]}`)
}

func (h *hub) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.ids...)
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
	h := &hub{status: func(int32, int) (int, string, map[string]string) {
		return 200, `{"results":[{"id":"a","status":"accepted"},{"id":"b","status":"duplicate"},{"id":"c","status":"rejected","code":"unknown_kind","message":"nope"}]}`, nil
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
	if h.calls.Load() != 1 {
		t.Fatalf("rejected item must not be retried, calls = %d", h.calls.Load())
	}
}

func TestRetryTransientThenDeliver(t *testing.T) {
	h := &hub{status: func(call int32, _ int) (int, string, map[string]string) {
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
		t.Fatalf("ids = %v (must be delivered exactly once)", got)
	}
}

func TestBadRequestDropsBatchWithoutRetry(t *testing.T) {
	h := &hub{status: func(int32, int) (int, string, map[string]string) { return 400, "", nil }}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, _ := New(fast(Config{URL: srv.URL}))
	c.Publish(item("a", PriorityHigh))
	waitFor(t, "reject", func() bool { return c.Stats().Rejected == 1 })
	closeClient(t, c)
	if h.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", h.calls.Load())
	}
}

func TestTooLargeSplitsBatch(t *testing.T) {
	h := &hub{status: func(_ int32, n int) (int, string, map[string]string) {
		if n > 2 {
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

func TestFullQueueDropsLowPriorityFirst(t *testing.T) {
	var drops atomic.Int32
	c, _ := New(Config{URL: "http://127.0.0.1:1", QueueSize: 3, BatchSize: 100, FlushInterval: time.Hour,
		OnDrop: func(n int, _ string) { drops.Add(int32(n)) }})
	c.Publish(item("low1", PriorityLow))
	c.Publish(item("high1", PriorityHigh))
	c.Publish(item("low2", PriorityLow))
	c.Publish(item("high2", PriorityHigh)) // evicts low1
	c.Publish(item("high3", PriorityHigh)) // evicts low2
	c.mu.Lock()
	var ids []string
	for _, it := range c.mem {
		ids = append(ids, it.ID)
	}
	c.mu.Unlock()
	if got := strings.Join(ids, ","); got != "high1,high2,high3" {
		t.Fatalf("queue = %s", got)
	}
	if drops.Load() != 2 {
		t.Fatalf("drops = %d", drops.Load())
	}
}

func TestSpoolOverflowKeepsOrder(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	h := &hub{status: func(int32, int) (int, string, map[string]string) {
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
	if _, err := os.Stat(filepath.Join(dir, "spool.jsonl")); err != nil {
		t.Fatal(err)
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

func TestSpoolCapDropsLowPriorityFirst(t *testing.T) {
	sp, err := openSpool(t.TempDir(), 400)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for i := 0; i < 10; i++ {
		p := PriorityHigh
		if i%2 == 0 {
			p = PriorityLow
		}
		n, err := sp.append(item(fmt.Sprintf("e%d", i), p))
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total == 0 || sp.pending() > 400 {
		t.Fatalf("cap not enforced: dropped=%d pending=%d", total, sp.pending())
	}
	items, _ := sp.readAll()
	lows := 0
	for _, it := range items {
		if it.Priority == PriorityLow {
			lows++
		}
	}
	if highs := len(items) - lows; highs < lows {
		t.Fatalf("low priority must go first: highs=%d lows=%d", highs, lows)
	}
}

func TestInvalidJSONIsDropped(t *testing.T) {
	var reason atomic.Value
	c, _ := New(Config{URL: "http://127.0.0.1:1", OnDrop: func(_ int, r string) { reason.Store(r) }})
	c.Publish(Item{ID: "x", Body: json.RawMessage(`{not json`)})
	closeClient(t, c)
	if reason.Load() != "invalid" {
		t.Fatalf("reason = %v", reason.Load())
	}
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
