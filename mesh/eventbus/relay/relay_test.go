package relay

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/hollis-labs/libs/mesh/eventbus"
)

// ---- fakes ----

func entry(seq int64) Entry {
	id := fmt.Sprintf("e%03d", seq)
	return Entry{Seq: seq, Item: eventbus.Item{ID: id, Priority: eventbus.PriorityHigh, Body: json.RawMessage(fmt.Sprintf(`{"id":%q}`, id))}}
}

// outbox is an in-memory Source that more entries can be appended to.
type outbox struct {
	mu      sync.Mutex
	entries []Entry
	fail    func(call int) error
	calls   int
	afters  []int64
}

func (o *outbox) add(seqs ...int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, s := range seqs {
		o.entries = append(o.entries, entry(s))
	}
}

func (o *outbox) After(_ context.Context, after int64, limit int) ([]Entry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	o.afters = append(o.afters, after)
	if o.fail != nil {
		if err := o.fail(o.calls); err != nil {
			return nil, err
		}
	}
	var out []Entry
	for _, e := range o.entries {
		if e.Seq > after {
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

type memCursor struct {
	mu      sync.Mutex
	seq     int64
	saves   []int64
	failing atomic.Bool
}

func (c *memCursor) Load(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq, nil
}

func (c *memCursor) Save(_ context.Context, seq int64) error {
	if c.failing.Load() {
		return errors.New("disk full")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq = seq
	c.saves = append(c.saves, seq)
	return nil
}

func (c *memCursor) get() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.seq }

// scripted is a Deliverer that answers from a script and records what it got.
type scripted struct {
	mu    sync.Mutex
	calls [][]string
	reply func(call int, items []eventbus.Item) []eventbus.Result
}

func (s *scripted) Deliver(_ context.Context, items []eventbus.Item) []eventbus.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	s.calls = append(s.calls, ids)
	return s.reply(len(s.calls), items)
}

func allSent(_ int, items []eventbus.Item) []eventbus.Result {
	out := make([]eventbus.Result, len(items))
	for i := range out {
		out[i] = eventbus.Result{Disposition: eventbus.DispositionSent}
	}
	return out
}

func newRelay(t *testing.T, cfg Config) *Relay {
	t.Helper()
	if cfg.Poll == 0 {
		cfg.Poll = 5 * time.Millisecond
	}
	if cfg.ErrorBackoff == 0 {
		cfg.ErrorBackoff = time.Millisecond
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = 5 * time.Millisecond
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// start runs the relay and returns a stop function that waits for Run to end.
func start(t *testing.T, r *Relay) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	return func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return after cancel")
			return nil
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// ---- behaviour ----

func TestNewRequiresItsParts(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("want an error")
	}
}

func TestRelaysInOrderAndAdvancesTheCursor(t *testing.T) {
	ob := &outbox{}
	ob.add(1, 2, 3, 4, 5)
	cur := &memCursor{}
	d := &scripted{reply: allSent}
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: d, BatchSize: 2})
	stop := start(t, r)
	waitFor(t, "cursor 5", func() bool { return cur.get() == 5 })
	if err := stop(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	var flat []string
	for _, c := range d.calls {
		flat = append(flat, c...)
	}
	if got := strings.Join(flat, ","); got != "e001,e002,e003,e004,e005" {
		t.Fatalf("delivered = %s", got)
	}
	if st := r.Stats(); st.Cursor != 5 || st.Sent != 5 || st.Quarantined != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestPicksUpEntriesAddedLaterAndWakeCutsTheWait(t *testing.T) {
	ob := &outbox{}
	cur := &memCursor{}
	d := &scripted{reply: allSent}
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: d, Poll: time.Hour})
	stop := start(t, r)
	time.Sleep(30 * time.Millisecond) // idle, waiting an hour
	ob.add(1)
	r.Wake()
	waitFor(t, "cursor 1 after Wake", func() bool { return cur.get() == 1 })
	_ = stop()
}

func TestResumesAfterTheSavedCursor(t *testing.T) {
	ob := &outbox{}
	ob.add(1, 2, 3, 4)
	cur := &memCursor{seq: 2}
	d := &scripted{reply: allSent}
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: d})
	stop := start(t, r)
	waitFor(t, "cursor 4", func() bool { return cur.get() == 4 })
	_ = stop()
	if got := strings.Join(d.calls[0], ","); got != "e003,e004" {
		t.Fatalf("first batch = %s, want only what follows the saved cursor", got)
	}
}

// A quarantined entry is durably set aside, so the relay moves past it instead
// of wedging.
func TestQuarantinedEntriesDoNotWedgeTheRelay(t *testing.T) {
	ob := &outbox{}
	ob.add(1, 2, 3)
	cur := &memCursor{}
	d := &scripted{reply: func(_ int, items []eventbus.Item) []eventbus.Result {
		out := make([]eventbus.Result, len(items))
		for i, it := range items {
			out[i] = eventbus.Result{Disposition: eventbus.DispositionSent}
			if it.ID == "e002" {
				out[i] = eventbus.Result{Disposition: eventbus.DispositionQuarantined, Reason: "rejected", Code: "unknown_kind"}
			}
		}
		return out
	}}
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: d})
	stop := start(t, r)
	waitFor(t, "cursor 3", func() bool { return cur.get() == 3 })
	_ = stop()
	if st := r.Stats(); st.Sent != 2 || st.Quarantined != 1 || st.Rejected != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

// Only the settled prefix moves the cursor; the unsettled tail is sent again by
// the next run.
func TestCursorStopsAtTheFirstUnsettledEntry(t *testing.T) {
	ob := &outbox{}
	ob.add(1, 2, 3, 4)
	cur := &memCursor{}
	first := true
	var cancelRun context.CancelFunc
	d := &scripted{reply: func(_ int, items []eventbus.Item) []eventbus.Result {
		out := make([]eventbus.Result, len(items))
		for i := range out {
			out[i] = eventbus.Result{Disposition: eventbus.DispositionSent}
		}
		if first {
			first = false
			out[2], out[3] = eventbus.Result{}, eventbus.Result{} // Pending
			cancelRun()                                           // the call ends before everything is settled
		}
		return out
	}}
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: d})
	ctx, cancel := context.WithCancel(context.Background())
	cancelRun = cancel
	if err := r.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if cur.get() != 2 {
		t.Fatalf("cursor = %d, want 2 (e003 and e004 were not settled)", cur.get())
	}
	// A new relay on the same cursor resumes at e003.
	d2 := &scripted{reply: allSent}
	r2 := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: d2})
	stop := start(t, r2)
	waitFor(t, "cursor 4", func() bool { return cur.get() == 4 })
	_ = stop()
	if got := strings.Join(d2.calls[0], ","); got != "e003,e004" {
		t.Fatalf("second run's first batch = %s", got)
	}
}

// A cursor that cannot be saved is reported and retried; the relay keeps going,
// and a restart in between resends events rather than losing any.
func TestCursorSaveFailureIsReportedRetriedAndNeverLosesEvents(t *testing.T) {
	ob := &outbox{}
	ob.add(1, 2, 3)
	cur := &memCursor{}
	cur.failing.Store(true)
	var errs atomic.Int32
	d := &scripted{reply: allSent}
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: d, OnError: func(error) { errs.Add(1) }})
	stop := start(t, r)
	waitFor(t, "a reported save failure", func() bool { return errs.Load() > 0 })
	waitFor(t, "all three sent", func() bool { return r.Stats().Sent == 3 })
	if cur.get() != 0 {
		t.Fatalf("nothing could be saved yet, cursor = %d", cur.get())
	}
	// Storage recovers: the relay saves the latest cursor on its own.
	cur.failing.Store(false)
	waitFor(t, "the cursor to catch up", func() bool { return cur.get() == 3 })
	_ = stop()

	// And if it had crashed with the cursor still at 0, a new run resends all
	// three (the hub answers duplicate); nothing is skipped.
	cur2 := &memCursor{}
	d2 := &scripted{reply: allSent}
	r2 := newRelay(t, Config{Source: ob, Cursor: cur2, Deliverer: d2})
	stop2 := start(t, r2)
	waitFor(t, "resend", func() bool { return cur2.get() == 3 })
	_ = stop2()
	if got := strings.Join(d2.calls[0], ","); got != "e001,e002,e003" {
		t.Fatalf("after a crash the relay must resend from the saved cursor: %s", got)
	}
}

func TestSourceErrorsAreReportedAndRetried(t *testing.T) {
	ob := &outbox{fail: func(call int) error {
		if call <= 2 {
			return errors.New("database is locked")
		}
		return nil
	}}
	ob.add(1)
	cur := &memCursor{}
	var errs atomic.Int32
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: &scripted{reply: allSent}, OnError: func(error) { errs.Add(1) }})
	stop := start(t, r)
	waitFor(t, "cursor 1", func() bool { return cur.get() == 1 })
	_ = stop()
	if errs.Load() < 2 {
		t.Fatalf("errors reported = %d, want the two source failures", errs.Load())
	}
}

// A source that returns entries out of order is a bug to report, not something
// to follow: the cursor must not move backwards or spin on the same rows.
func TestOutOfOrderSourceIsReportedAndNotFollowed(t *testing.T) {
	ob := &outbox{}
	ob.add(3, 2) // wrong order
	cur := &memCursor{}
	var errs atomic.Int32
	d := &scripted{reply: allSent}
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: d, OnError: func(error) { errs.Add(1) }})
	stop := start(t, r)
	waitFor(t, "a reported ordering error", func() bool { return errs.Load() >= 2 })
	_ = stop()
	if len(d.calls) != 0 || cur.get() != 0 {
		t.Fatalf("nothing may be delivered or saved from a misordered batch: calls=%v cursor=%d", d.calls, cur.get())
	}
}

func TestRunRefusesToStartWithoutAReadableCursor(t *testing.T) {
	r := newRelay(t, Config{Source: &outbox{}, Cursor: badCursor{}, Deliverer: &scripted{reply: allSent}})
	if err := r.Run(context.Background()); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want a load error", err)
	}
}

type badCursor struct{}

func (badCursor) Load(context.Context) (int64, error) { return 0, errors.New("unreadable") }
func (badCursor) Save(context.Context, int64) error   { return nil }

func TestRunTwiceAtOnceIsRefused(t *testing.T) {
	r := newRelay(t, Config{Source: &outbox{}, Cursor: &memCursor{}, Deliverer: &scripted{reply: allSent}})
	stop := start(t, r)
	time.Sleep(20 * time.Millisecond)
	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a second Run must be refused")
	}
	_ = stop()
}

// ---- FileCursor ----

func TestFileCursor(t *testing.T) {
	f := FileCursor{Path: filepath.Join(t.TempDir(), "sub", "cursor")}
	if n, err := f.Load(context.Background()); err != nil || n != 0 {
		t.Fatalf("missing file: %d %v", n, err)
	}
	if err := f.Save(context.Background(), 41); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if n, err := f.Load(context.Background()); err != nil || n != 42 {
		t.Fatalf("load: %d %v", n, err)
	}
	if _, err := os.Stat(f.Path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("the temporary file must not be left behind")
	}
	if err := os.WriteFile(f.Path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Load(context.Background()); err == nil {
		t.Fatal("a corrupt cursor file must be an error, not zero (zero would resend everything)")
	}
}

// ---- with the real client ----

type hub struct {
	mu   sync.Mutex
	seen map[string]bool
	ids  []string
	rej  map[string]bool
}

func (h *hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Events []struct {
			ID string `json:"id"`
		} `json:"events"`
	}
	_ = json.Unmarshal(raw, &req)
	var parts []string
	h.mu.Lock()
	for _, e := range req.Events {
		switch {
		case h.rej[e.ID]:
			parts = append(parts, fmt.Sprintf(`{"id":%q,"status":"rejected","code":"unknown_kind"}`, e.ID))
		case h.seen[e.ID]:
			parts = append(parts, fmt.Sprintf(`{"id":%q,"status":"duplicate","cursor":"1"}`, e.ID))
		default:
			h.seen[e.ID] = true
			h.ids = append(h.ids, e.ID)
			parts = append(parts, fmt.Sprintf(`{"id":%q,"status":"accepted","cursor":"1"}`, e.ID))
		}
	}
	h.mu.Unlock()
	w.WriteHeader(200)
	_, _ = io.WriteString(w, `{"results":[`+strings.Join(parts, ",")+`]}`)
}

func newHub() *hub { return &hub{seen: map[string]bool{}, rej: map[string]bool{}} }

func (h *hub) got() string { h.mu.Lock(); defer h.mu.Unlock(); return strings.Join(h.ids, ",") }

func realClient(t *testing.T, url, dir string) *eventbus.Client {
	t.Helper()
	c, err := eventbus.New(eventbus.Config{URL: url, SpoolDir: dir, FlushInterval: time.Hour,
		BackoffBase: time.Millisecond, BackoffMax: 5 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})
	return c
}

// End to end: the real client, an outbox, a file cursor. A hub that is down for a
// while delays delivery but loses nothing; a rejected event is quarantined and
// the relay moves on; a restarted relay does not resend settled events.
func TestEndToEndWithTheRealClient(t *testing.T) {
	h := newHub()
	h.rej["e003"] = true
	var down atomic.Bool
	down.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(503)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := realClient(t, srv.URL, dir)

	ob := &outbox{}
	ob.add(1, 2, 3, 4, 5)
	cur := FileCursor{Path: filepath.Join(dir, "relay.cursor")}
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: c, BatchSize: 3})
	stop := start(t, r)

	time.Sleep(60 * time.Millisecond)
	if n, _ := cur.Load(context.Background()); n != 0 || r.Stats().Sent != 0 {
		t.Fatalf("the hub is down: nothing may be settled (cursor %d, stats %+v)", n, r.Stats())
	}
	down.Store(false)
	waitFor(t, "all five settled", func() bool { n, _ := cur.Load(context.Background()); return n == 5 })
	_ = stop()
	if got := h.got(); got != "e001,e002,e004,e005" {
		t.Fatalf("hub accepted %s, want everything but the rejected e003, in order", got)
	}
	if st := r.Stats(); st.Sent != 4 || st.Quarantined != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if q, err := os.ReadFile(filepath.Join(dir, "quarantine.jsonl")); err != nil || !strings.Contains(string(q), `"e003"`) {
		t.Fatalf("the rejected event must be in the client's quarantine: %v %s", err, q)
	}

	// A restarted relay (new Relay, same cursor file) resends nothing.
	before := len(h.ids)
	ob.add(6)
	r2 := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: c})
	stop2 := start(t, r2)
	waitFor(t, "e006", func() bool { n, _ := cur.Load(context.Background()); return n == 6 })
	_ = stop2()
	if got := h.got(); got != "e001,e002,e004,e005,e006" || len(h.ids) != before+1 {
		t.Fatalf("hub got %s; the restart must send only e006", got)
	}
}

// ---- rule tests with the real client ----

func (o *outbox) Head(context.Context) (int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.entries) == 0 {
		return 0, nil
	}
	return o.entries[len(o.entries)-1].Seq, nil
}

func clientWith(t *testing.T, cfg eventbus.Config) *eventbus.Client {
	t.Helper()
	cfg.FlushInterval = time.Hour
	cfg.BackoffBase = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := eventbus.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})
	return c
}

// The relay crashed after the hub settled the events but before the cursor was
// saved: the next run sends them again, the hub says duplicate, and nothing is
// skipped.
func TestCrashBetweenDeliverAndSaveResendsAndSkipsNothing(t *testing.T) {
	h := newHub()
	srv := httptest.NewServer(h)
	defer srv.Close()
	c := clientWith(t, eventbus.Config{URL: srv.URL})

	ob := &outbox{}
	ob.add(1, 2, 3)
	lost := &memCursor{} // a cursor whose saves never land: the process "crashes" before them
	lost.failing.Store(true)
	r1 := newRelay(t, Config{Source: ob, Cursor: lost, Deliverer: c})
	stop1 := start(t, r1)
	waitFor(t, "first run delivered", func() bool { return r1.Stats().Sent == 3 })
	_ = stop1()
	if lost.get() != 0 {
		t.Fatalf("setup: cursor must not have been saved, is %d", lost.get())
	}

	// Restart: same (unsaved) cursor, same outbox, same hub.
	cur := &memCursor{}
	r2 := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: c})
	stop2 := start(t, r2)
	waitFor(t, "second run caught up", func() bool { return cur.get() == 3 })
	_ = stop2()
	if st := r2.Stats(); st.Sent != 3 || st.Duplicates != 3 {
		t.Fatalf("the resend must be reported as duplicates: %+v", st)
	}
	if got := h.got(); got != "e001,e002,e003" {
		t.Fatalf("hub holds %s: each event exactly once, in order", got)
	}
}

// 503 and 429 only delay: the cursor does not move until the hub settles.
func TestHubFailuresDelayButDoNotAdvanceTheCursor(t *testing.T) {
	h := newHub()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1, 2:
			w.WriteHeader(503)
		case 3:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
		default:
			h.ServeHTTP(w, r)
		}
	}))
	defer srv.Close()
	c := clientWith(t, eventbus.Config{URL: srv.URL})
	ob := &outbox{}
	ob.add(1, 2)
	cur := &memCursor{}
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: c})
	stop := start(t, r)
	waitFor(t, "delivery after the failures", func() bool { return cur.get() == 2 })
	_ = stop()
	if got := h.got(); got != "e001,e002" {
		t.Fatalf("hub holds %s", got)
	}
	for _, s := range cur.saves {
		if s == 0 {
			t.Fatal("the cursor was saved before anything was settled")
		}
	}
}

// A rejected event advances the cursor only once it is durably quarantined. If
// the quarantine cannot be written the relay stays put, reports, and moves on
// when storage works.
func TestRejectedEventDoesNotAdvanceUntilItIsDurablyQuarantined(t *testing.T) {
	h := newHub()
	h.rej["e002"] = true
	srv := httptest.NewServer(h)
	defer srv.Close()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "quarantine.jsonl"), 0o700); err != nil { // storage is broken
		t.Fatal(err)
	}
	var errs atomic.Int32
	c := clientWith(t, eventbus.Config{URL: srv.URL, SpoolDir: dir, OnError: func(error) { errs.Add(1) }})
	ob := &outbox{}
	ob.add(1, 2, 3)
	cur := &memCursor{}
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: c})
	stop := start(t, r)
	waitFor(t, "a reported storage failure", func() bool { return errs.Load() > 0 })
	time.Sleep(80 * time.Millisecond)
	// Deliver holds the whole batch until e002 can be recorded, so the cursor
	// stays where it was: it never moves past a rejected event that is not yet
	// durably quarantined. (A restart would resend e001, which the hub dedupes.)
	if c := cur.get(); c >= 2 {
		t.Fatalf("cursor = %d: it moved past e002 although e002 could not be recorded", c)
	}
	if err := os.Remove(filepath.Join(dir, "quarantine.jsonl")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cursor 3 once storage works", func() bool { return cur.get() == 3 })
	_ = stop()
	if q, _ := os.ReadFile(filepath.Join(dir, "quarantine.jsonl")); !strings.Contains(string(q), `"e002"`) {
		t.Fatalf("e002 must be in the quarantine file: %s", q)
	}
	if st := r.Stats(); st.Rejected != 1 || st.Quarantined != 1 || st.Sent != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

// A missing credential must not drain the outbox into quarantine: the relay
// waits, then delivers everything once the credential exists.
func TestMissingCredentialIsWaitedOutNotQuarantined(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	h := newHub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer issued" {
			w.WriteHeader(401)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := clientWith(t, eventbus.Config{URL: srv.URL, TokenFile: tokenFile, SpoolDir: dir, RetryAuthMax: 20 * time.Millisecond, BatchSize: 1})
	ob := &outbox{}
	ob.add(1, 2, 3, 4, 5, 6)
	cur := &memCursor{}
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: c, BatchSize: 2})
	stop := start(t, r)
	time.Sleep(200 * time.Millisecond) // ten times the ceiling
	if cur.get() != 0 || r.Stats().Quarantined != 0 {
		t.Fatalf("nothing may be quarantined or moved past: cursor %d, stats %+v", cur.get(), r.Stats())
	}
	if err := os.WriteFile(tokenFile, []byte("issued\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "everything delivered", func() bool { return cur.get() == 6 })
	_ = stop()
	if got := h.got(); got != "e001,e002,e003,e004,e005,e006" {
		t.Fatalf("hub holds %s", got)
	}
	if q, _ := os.ReadFile(filepath.Join(dir, "quarantine.jsonl")); len(q) != 0 {
		t.Fatalf("quarantine must be empty: %s", q)
	}
}

// Closing the client under a running relay stops delivery cleanly: Close returns,
// the relay keeps its cursor where it was and stops when its context ends.
func TestClosingTheClientUnderARunningRelay(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	c, err := eventbus.New(eventbus.Config{URL: srv.URL, Timeout: time.Hour, FlushInterval: time.Hour,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ob := &outbox{}
	ob.add(1, 2)
	cur := &memCursor{}
	var errs atomic.Int32
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: c, OnError: func(error) { errs.Add(1) }})
	stop := start(t, r)
	time.Sleep(60 * time.Millisecond) // Deliver is blocked inside the request

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- c.Close(ctx) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return while a relay was delivering")
	}
	waitFor(t, "the relay to report that the client is closed", func() bool { return errs.Load() > 0 })
	if cur.get() != 0 {
		t.Fatalf("cursor = %d: nothing was settled", cur.get())
	}
	if err := stop(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
}

// Lag is reported when the source can say where the head is.
func TestStatsReportLagWhenTheSourceHasAHead(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	h := newHub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(503)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := clientWith(t, eventbus.Config{URL: srv.URL})
	ob := &outbox{}
	ob.add(1, 2, 3, 4)
	cur := &memCursor{}
	r := newRelay(t, Config{Source: ob, Cursor: cur, Deliverer: c})
	stop := start(t, r)
	waitFor(t, "lag visible while the hub is down", func() bool { return r.Stats().Lag == 4 })
	down.Store(false)
	waitFor(t, "lag cleared", func() bool { st := r.Stats(); return st.Cursor == 4 && st.Lag == 0 })
	_ = stop()
	if st := r.Stats(); st.Head != 4 || st.Sent != 4 {
		t.Fatalf("stats = %+v", st)
	}
}
