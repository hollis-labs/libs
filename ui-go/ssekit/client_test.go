package ssekit_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	ssekit "github.com/hollis-labs/go-ssekit"
	"github.com/hollis-labs/go-ssekit/ssetest"
)

// fast retries: no jitter, 1 ms.
var fast = ssekit.WithBackoff([]time.Duration{time.Millisecond}, 0)

type harness struct {
	t      *testing.T
	script *ssetest.Scripted
	srv    *httptest.Server
	client *ssekit.Client
	mu     sync.Mutex
	seen   []string // lastEventID handed to newReq
}

func newHarness(t *testing.T, steps ...ssetest.Step) *harness {
	t.Helper()
	noLeaks(t) // registered first, so it runs after the server is closed
	h := &harness{t: t, script: ssetest.Script(steps...)}
	h.srv = httptest.NewServer(h.script)
	t.Cleanup(h.srv.Close)
	h.client = ssekit.NewClient(h.srv.Client())
	return h
}

func (h *harness) newReq(lastEventID string) (*http.Request, error) {
	h.mu.Lock()
	h.seen = append(h.seen, lastEventID)
	h.mu.Unlock()
	return http.NewRequest(http.MethodGet, h.srv.URL, nil)
}

func (h *harness) ids(evs []ssekit.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

// run consumes the stream to its end, with a safety timeout.
func (h *harness) run(o ...ssekit.StreamOption) ([]ssekit.Event, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.t.Context(), 20*time.Second)
	defer cancel()
	var evs []ssekit.Event
	for ev, err := range h.client.Stream(ctx, h.newReq, o...) {
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func term(id string) ssekit.StreamOption {
	return ssekit.WithIsTerminal(func(e ssekit.Event) bool { return e.ID == id })
}

func TestStream_DropReconnectsWithLastEventID(t *testing.T) {
	h := newHarness(t, ssetest.Drop(3), ssetest.Emit(3))
	evs, err := h.run(fast, term("6"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := h.ids(evs), []string{"1", "2", "3", "4", "5", "6"}; !sameStrings(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	reqs := h.script.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	if reqs[0].LastEventID != "" || reqs[1].LastEventID != "3" {
		t.Fatalf("Last-Event-ID header = %q then %q, want \"\" then \"3\"", reqs[0].LastEventID, reqs[1].LastEventID)
	}
	if got, want := h.seen, []string{"", "3"}; !sameStrings(got, want) {
		t.Fatalf("newReq saw %v, want %v (apps that resume by query parameter need it)", got, want)
	}
	if a := reqs[0].Header.Get("Accept"); a != "text/event-stream" {
		t.Fatalf("Accept = %q", a)
	}
	if string(evs[0].Data) != "payload-1" || evs[0].Name != "tick" {
		t.Fatalf("first event = %+v", evs[0])
	}
}

func TestStream_CleanCloseAlsoReconnects(t *testing.T) {
	h := newHarness(t, ssetest.Emit(2), ssetest.Close(), ssetest.Emit(2))
	var causes []error
	evs, err := h.run(fast, term("4"), ssekit.WithOnReconnect(func(r ssekit.Reconnect) { causes = append(causes, r.Err) }))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 4 || len(causes) != 1 || !errors.Is(causes[0], io.EOF) {
		t.Fatalf("events %d, causes %v", len(evs), causes)
	}
}

func TestStream_OverlapPassesThroughAndContinuityCanAcceptIt(t *testing.T) {
	h := newHarness(t, ssetest.Drop(3), ssetest.Overlap(2), ssetest.Emit(3))
	// Server replays ids 2,3,4 after the client had 1,2,3.
	within := func(prev, next string) bool { // next may repeat or advance by one
		p, _ := strconv.Atoi(prev)
		n, _ := strconv.Atoi(next)
		return n <= p+1
	}
	evs, err := h.run(fast, term("4"), ssekit.WithContinuity(within))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := h.ids(evs), []string{"1", "2", "3", "2", "3", "4"}; !sameStrings(got, want) {
		t.Fatalf("ids = %v, want %v: the library delivers what arrives; de-duplication is the application's call", got, want)
	}
	// The application's de-duplication is a few lines over the id it understands.
	var applied []string
	high := 0
	for _, e := range evs {
		n, _ := strconv.Atoi(e.ID)
		if n > high {
			high = n
			applied = append(applied, e.ID)
		}
	}
	if !sameStrings(applied, []string{"1", "2", "3", "4"}) {
		t.Fatalf("applied = %v", applied)
	}
}

func TestStream_GapYieldsGapError(t *testing.T) {
	h := newHarness(t, ssetest.Drop(2), ssetest.Gap(3, 5), ssetest.Emit(2))
	contiguous := func(prev, next string) bool {
		p, _ := strconv.Atoi(prev)
		n, _ := strconv.Atoi(next)
		return n <= p+1
	}
	evs, err := h.run(fast, ssekit.WithContinuity(contiguous))
	var ge *ssekit.GapError
	if !errors.As(err, &ge) {
		t.Fatalf("err = %v, want *GapError", err)
	}
	if ge.Prev != "2" || ge.Next != "6" || ge.Event.ID != "6" {
		t.Fatalf("gap = %+v", ge)
	}
	if got := h.ids(evs); !sameStrings(got, []string{"1", "2"}) {
		t.Fatalf("delivered %v; the event after the gap must not be delivered", got)
	}
}

func TestStream_NoContinuityCheckMeansNoGapDetection(t *testing.T) {
	h := newHarness(t, ssetest.Drop(2), ssetest.Gap(3, 5), ssetest.Emit(2))
	evs, err := h.run(fast, term("7"))
	if err != nil || len(evs) != 4 {
		t.Fatalf("%d events, %v", len(evs), err)
	}
}

func TestStream_Status404IsFinal(t *testing.T) {
	h := newHarness(t, ssetest.Status(404), ssetest.Emit(1))
	_, err := h.run(fast)
	var se *ssekit.StatusError
	if !errors.As(err, &se) || se.Code != 404 {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.script.Requests()); n != 1 {
		t.Fatalf("requests = %d, a final status must not be retried", n)
	}
}

func TestStream_Status503And429AreRetried(t *testing.T) {
	h := newHarness(t, ssetest.Status(503), ssetest.Status(429), ssetest.Status(408), ssetest.Emit(2))
	var recon []ssekit.Reconnect
	evs, err := h.run(fast, term("2"), ssekit.WithOnReconnect(func(r ssekit.Reconnect) { recon = append(recon, r) }))
	if err != nil || len(evs) != 2 {
		t.Fatalf("%d events, %v", len(evs), err)
	}
	if len(recon) != 3 || recon[0].Attempt != 1 || recon[2].Attempt != 3 {
		t.Fatalf("reconnects = %+v", recon)
	}
	var se *ssekit.StatusError
	if !errors.As(recon[0].Err, &se) || se.Code != 503 {
		t.Fatalf("cause = %v", recon[0].Err)
	}
}

func TestStream_CustomFinalStatus(t *testing.T) {
	h := newHarness(t, ssetest.Status(503))
	_, err := h.run(fast, ssekit.WithIsFinalStatus(func(c int) bool { return c == 503 }))
	var se *ssekit.StatusError
	if !errors.As(err, &se) || se.Code != 503 {
		t.Fatalf("err = %v", err)
	}
}

func TestStream_ExhaustedScriptIs410AndFinal(t *testing.T) {
	h := newHarness(t, ssetest.Emit(1), ssetest.Close())
	evs, err := h.run(fast)
	var se *ssekit.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusGone || len(evs) != 1 {
		t.Fatalf("%d events, %v", len(evs), err)
	}
}

func TestStream_204EndsWithoutError(t *testing.T) {
	h := newHarness(t, ssetest.Status(204))
	evs, err := h.run(fast)
	if err != nil || len(evs) != 0 {
		t.Fatalf("%d events, %v", len(evs), err)
	}
	if n := len(h.script.Requests()); n != 1 {
		t.Fatalf("requests = %d", n)
	}
}

func TestStream_WrongContentTypeIsFinal(t *testing.T) {
	noLeaks(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	defer srv.Close()
	c := ssekit.NewClient(srv.Client())
	for _, err := range c.Stream(t.Context(), func(string) (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	}, fast) {
		if !errors.Is(err, ssekit.ErrNotEventStream) {
			t.Fatalf("err = %v", err)
		}
		return
	}
	t.Fatal("no error yielded")
}

func TestStream_StallTripsTheIdleWatchdog(t *testing.T) {
	h := newHarness(t, ssetest.Emit(1), ssetest.Stall(30*time.Second), ssetest.Emit(1))
	var causes []error
	start := time.Now()
	evs, err := h.run(fast, term("2"), ssekit.WithIdleTimeout(80*time.Millisecond),
		ssekit.WithOnReconnect(func(r ssekit.Reconnect) { causes = append(causes, r.Err) }))
	if err != nil || len(evs) != 2 {
		t.Fatalf("%d events, %v", len(evs), err)
	}
	if len(causes) != 1 || !errors.Is(causes[0], ssekit.ErrIdle) {
		t.Fatalf("causes = %v, want [ErrIdle]", causes)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the stall was not cut short")
	}
	if reqs := h.script.Requests(); len(reqs) != 2 || reqs[1].LastEventID != "1" {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestStream_CommentsKeepTheWatchdogQuiet(t *testing.T) {
	steps := []ssetest.Step{ssetest.Emit(1)}
	for range 8 { // 8 x 30 ms of comments: well over the 100 ms idle timeout in total
		steps = append(steps, ssetest.Comment("keepalive"), ssetest.Wait(30*time.Millisecond))
	}
	steps = append(steps, ssetest.Emit(1))
	h := newHarness(t, steps...)
	evs, err := h.run(fast, term("2"), ssekit.WithIdleTimeout(100*time.Millisecond))
	if err != nil || len(evs) != 2 {
		t.Fatalf("%d events, %v", len(evs), err)
	}
	if n := len(h.script.Requests()); n != 1 {
		t.Fatalf("requests = %d: comments must count as activity", n)
	}
}

func TestStream_SlowConsumerIsNotIdle(t *testing.T) {
	h := newHarness(t, ssetest.Emit(3), ssetest.Close())
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var causes []error
	n := 0
	for _, err := range h.client.Stream(ctx, h.newReq, fast, ssekit.WithIdleTimeout(60*time.Millisecond),
		ssekit.WithOnReconnect(func(r ssekit.Reconnect) { causes = append(causes, r.Err) })) {
		if err != nil {
			break // the 410 after the script ends
		}
		n++
		time.Sleep(120 * time.Millisecond) // slower than the idle timeout
	}
	if n != 3 {
		t.Fatalf("events = %d", n)
	}
	if len(causes) != 1 || !errors.Is(causes[0], io.EOF) {
		t.Fatalf("causes = %v: consumer slowness must not read as server silence", causes)
	}
}

func TestStream_BurstLosesNothingUnderBackpressure(t *testing.T) {
	const n = 20000
	h := newHarness(t, ssetest.Burst(n), ssetest.Close())
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	next := 1
	for ev, err := range h.client.Stream(ctx, h.newReq, fast, term(strconv.Itoa(n))) {
		if err != nil {
			t.Fatalf("after %d events: %v", next-1, err)
		}
		if ev.ID != strconv.Itoa(next) {
			t.Fatalf("event %d has id %s", next, ev.ID)
		}
		next++
		if next%5000 == 0 {
			time.Sleep(20 * time.Millisecond) // a consumer slower than the writer
		}
	}
	if next != n+1 {
		t.Fatalf("received %d of %d", next-1, n)
	}
	if got := len(h.script.Requests()); got != 1 {
		t.Fatalf("requests = %d; a slow consumer must not cause reconnects", got)
	}
}

func TestStream_ServerRetryIsHonoured(t *testing.T) {
	h := newHarness(t, ssetest.Retry(37*time.Millisecond), ssetest.Drop(1), ssetest.Emit(1))
	var delays []time.Duration
	// The schedule says an hour: only the server's retry: can make this finish.
	evs, err := h.run(ssekit.WithBackoff([]time.Duration{time.Hour}, 0), term("2"),
		ssekit.WithOnReconnect(func(r ssekit.Reconnect) { delays = append(delays, r.Delay) }))
	if err != nil || len(evs) != 2 {
		t.Fatalf("%d events, %v", len(evs), err)
	}
	if len(delays) != 1 || delays[0] != 37*time.Millisecond {
		t.Fatalf("delays = %v", delays)
	}
	if evs[0].Retry != 37*time.Millisecond {
		t.Fatalf("Event.Retry = %v", evs[0].Retry)
	}
}

func TestStream_BackoffScheduleAndReset(t *testing.T) {
	// Three failures in a row walk the schedule; an event resets it.
	h := newHarness(t, ssetest.Status(500), ssetest.Status(500), ssetest.Status(500), ssetest.Drop(1), ssetest.Status(500), ssetest.Emit(1))
	var delays []time.Duration
	_, err := h.run(ssekit.WithBackoff([]time.Duration{1 * time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}, 0), term("2"),
		ssekit.WithOnReconnect(func(r ssekit.Reconnect) { delays = append(delays, r.Delay) }))
	if err != nil {
		t.Fatal(err)
	}
	ms := time.Millisecond
	want := []time.Duration{1 * ms, 2 * ms, 4 * ms, 1 * ms, 2 * ms} // the event resets the count once
	if len(delays) != len(want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("delays = %v, want %v", delays, want)
		}
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The default schedule, observed on a virtual clock: an in-memory transport
// that answers 500 every time, so 31 virtual seconds cost no real time.
func TestStream_DefaultBackoffScheduleValues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hc := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Status: "500 Internal Server Error", Body: http.NoBody, Header: http.Header{}, Request: r}, nil
		})}
		c := ssekit.NewClient(hc)
		var delays []time.Duration
		start := time.Now()
		var last error
		for _, err := range c.Stream(t.Context(), func(string) (*http.Request, error) {
			return http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
		}, ssekit.WithMaxReconnects(6), ssekit.WithBackoff(nil, 0), // default schedule, no jitter
			ssekit.WithOnReconnect(func(r ssekit.Reconnect) { delays = append(delays, r.Delay) })) {
			last = err
		}
		s := time.Second
		want := []time.Duration{s, 2 * s, 4 * s, 8 * s, 16 * s, 16 * s}
		if len(delays) != len(want) {
			t.Fatalf("delays = %v", delays)
		}
		for i := range want {
			if delays[i] != want[i] {
				t.Fatalf("delays = %v, want %v", delays, want)
			}
		}
		if el := time.Since(start); el != 47*s {
			t.Fatalf("virtual elapsed = %v", el)
		}
		if last == nil {
			t.Fatal("expected the final error")
		}
	})
}

// The default jitter (20%) keeps every delay within its band.
func TestStream_DefaultJitterStaysInBand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hc := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Status: "500", Body: http.NoBody, Header: http.Header{}, Request: r}, nil
		})}
		var delays []time.Duration
		for range ssekit.NewClient(hc).Stream(t.Context(), func(string) (*http.Request, error) {
			return http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
		}, ssekit.WithMaxReconnects(20), ssekit.WithOnReconnect(func(r ssekit.Reconnect) { delays = append(delays, r.Delay) })) {
		}
		base := []time.Duration{1, 2, 4, 8, 16}
		differs := false
		for i, d := range delays {
			b := base[min(i, 4)] * time.Second
			if d < b*8/10 || d > b*12/10 {
				t.Fatalf("delay %d = %v outside 20%% of %v", i, d, b)
			}
			differs = differs || d != b
		}
		if !differs {
			t.Fatal("no jitter applied")
		}
	})
}

func TestStream_MaxReconnects(t *testing.T) {
	h := newHarness(t, ssetest.Status(500), ssetest.Status(500), ssetest.Status(500), ssetest.Status(500))
	_, err := h.run(fast, ssekit.WithMaxReconnects(2))
	var se *ssekit.StatusError
	if !errors.As(err, &se) || se.Code != 500 {
		t.Fatalf("err = %v, want the last cause wrapped", err)
	}
	if n := len(h.script.Requests()); n != 3 {
		t.Fatalf("requests = %d, want 1 + 2 reconnects", n)
	}

	h = newHarness(t, ssetest.Drop(1))
	if _, err := h.run(fast, ssekit.WithMaxReconnects(0)); err == nil {
		t.Fatal("expected an error with reconnecting disabled")
	}
	if n := len(h.script.Requests()); n != 1 {
		t.Fatalf("requests = %d", n)
	}
}

func TestStream_OversizeEventIsFinal(t *testing.T) {
	h := newHarness(t, ssetest.Raw("data: "+string(make([]byte, 5000))+"\n\n"), ssetest.Emit(1))
	_, err := h.run(fast, ssekit.WithStreamMaxEventBytes(1000))
	if !errors.Is(err, ssekit.ErrEventTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.script.Requests()); n != 1 {
		t.Fatalf("requests = %d; retrying an oversize event would loop", n)
	}
}

func TestStream_HostileFramingAcrossChunks(t *testing.T) {
	h := newHarness(t, ssetest.Raw("id: 1\r", "\nevent: a\r", "\ndata: x\r\n", "\r", "\n", ": c\r\r", "data: y\r\rid: 2\n", "data: z\n\n"), ssetest.Close())
	evs, err := h.run(fast, term("2"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || string(evs[0].Data) != "x" || evs[0].Name != "a" || string(evs[1].Data) != "y" || string(evs[2].Data) != "z" || evs[2].ID != "2" {
		t.Fatalf("events = %+v", evs)
	}
}

func TestNewClient_ZeroesTimeoutAndDoesNotMutateTheCaller(t *testing.T) {
	h := newHarness(t, ssetest.Emit(1), ssetest.Wait(300*time.Millisecond), ssetest.Emit(1))
	hc := h.srv.Client()
	hc.Timeout = 100 * time.Millisecond // would cut a 300 ms stream
	c := ssekit.NewClient(hc)
	if hc.Timeout != 100*time.Millisecond {
		t.Fatal("NewClient mutated the caller's client")
	}
	n := 0
	for ev, err := range c.Stream(t.Context(), h.newReq, fast, term("2"), ssekit.WithIdleTimeout(0)) {
		if err != nil {
			t.Fatal(err)
		}
		_ = ev
		n++
	}
	if n != 2 || len(h.script.Requests()) != 1 {
		t.Fatalf("events %d, requests %d: the client timeout leaked into the stream", n, len(h.script.Requests()))
	}
}

func TestNewClient_WithDefaultsAndOverride(t *testing.T) {
	h := newHarness(t, ssetest.Status(503), ssetest.Status(503), ssetest.Status(503))
	c := ssekit.NewClient(h.srv.Client(), ssekit.WithDefaults(fast, ssekit.WithMaxReconnects(1)))
	for _, err := range c.Stream(t.Context(), h.newReq) {
		if err == nil {
			t.Fatal("expected error")
		}
	}
	if n := len(h.script.Requests()); n != 2 {
		t.Fatalf("requests = %d, want the default MaxReconnects(1) to apply", n)
	}
	for _, err := range c.Stream(t.Context(), h.newReq, ssekit.WithMaxReconnects(0)) {
		_ = err
	}
	if n := len(h.script.Requests()); n != 3 {
		t.Fatalf("requests = %d, want the per-call option to override", n)
	}
}

func TestStream_ContextCancelAndEarlyBreakHangUp(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		h := newHarness(t, ssetest.Emit(1), ssetest.Stall(time.Minute))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var last error
		for _, err := range h.client.Stream(ctx, h.newReq, fast, ssekit.WithIdleTimeout(0)) {
			if err != nil {
				last = err
				break
			}
			cancel()
		}
		if !errors.Is(last, context.Canceled) {
			t.Fatalf("err = %v", last)
		}
	})
	t.Run("break", func(t *testing.T) {
		// srv.Close (a cleanup) waits for the handler; it returns only because
		// breaking out of the loop closed the connection.
		h := newHarness(t, ssetest.Emit(1), ssetest.Stall(time.Minute))
		for range h.client.Stream(t.Context(), h.newReq, fast, ssekit.WithIdleTimeout(0)) {
			break
		}
	})
}

func TestStream_NewReqErrorIsFinal(t *testing.T) {
	noLeaks(t)
	boom := errors.New("cannot build request")
	c := ssekit.NewClient(nil)
	for _, err := range c.Stream(t.Context(), func(string) (*http.Request, error) { return nil, boom }) {
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		return
	}
	t.Fatal("no error")
}

func TestStream_NewReqCanCarryTheCursorInTheURL(t *testing.T) {
	h := newHarness(t, ssetest.Drop(2), ssetest.Emit(1))
	newReq := func(last string) (*http.Request, error) {
		u := h.srv.URL + "/?from=" + last
		return http.NewRequest(http.MethodGet, u, nil)
	}
	for ev, err := range h.client.Stream(t.Context(), newReq, fast) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.ID == "3" {
			break
		}
	}
	reqs := h.script.Requests()
	if got := reqs[1].Query.Get("from"); got != "2" {
		t.Fatalf("second request from = %q", got)
	}
}
