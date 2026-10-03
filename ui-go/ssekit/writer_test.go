package ssekit_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	ssekit "github.com/hollis-labs/libs/ui-go/ssekit"
)

// The expected strings below are the bytes produced by testdata/capture, a
// program holding copies of the applications' fmt.Fprintf framing statements
// (run 2026-09-29; see that file for exactly what is and is not copied). They
// were not produced by running the applications themselves.
func TestSend_ByteCompatWithApplicationFraming(t *testing.T) {
	cases := []struct {
		name string
		ev   ssekit.Event
		want string
	}{
		{"nanite messages: id, event, data", ssekit.Event{ID: "12", Name: "text_delta", Data: []byte(`{"type":"text_delta"}`)},
			"id: 12\nevent: text_delta\ndata: {\"type\":\"text_delta\"}\n\n"},
		{"nanite messages: id-less control frame", ssekit.Event{Name: "session_takeover", Data: []byte(`{"type":"session_takeover"}`)},
			"event: session_takeover\ndata: {\"type\":\"session_takeover\"}\n\n"},
		{"nanite host feed: head frame has no id", ssekit.Event{Name: "host_runtime.head.v1", Data: []byte(`{"head":1}`)},
			"event: host_runtime.head.v1\ndata: {\"head\":1}\n\n"},
		{"nanite host feed: gap frame", ssekit.Event{ID: "7", Name: "host_runtime.gap.v1", Data: []byte(`{"head":1}`)},
			"id: 7\nevent: host_runtime.gap.v1\ndata: {\"head\":1}\n\n"},
		{"tangent revision", ssekit.Event{Name: "revision", Data: []byte(`{"revision":"r1"}`)},
			"event: revision\ndata: {\"revision\":\"r1\"}\n\n"},
		{"tether: id, kind", ssekit.Event{ID: "42", Name: "session.started", Data: []byte(`{"scope":"session"}`)},
			"id: 42\nevent: session.started\ndata: {\"scope\":\"session\"}\n\n"},
		{"tether: empty kind omits event line", ssekit.Event{ID: "43", Data: []byte(`{"scope":"daemon"}`)},
			"id: 43\ndata: {\"scope\":\"daemon\"}\n\n"},
		{"empty data", ssekit.Event{Name: "x"}, "event: x\ndata: \n\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rw := newMemWriter()
			w, err := ssekit.NewWriter(rw)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.Send(c.ev); err != nil {
				t.Fatal(err)
			}
			if got := rw.String(); got != c.want {
				t.Fatalf("wire = %q, want %q", got, c.want)
			}
		})
	}
}

func TestComment_ByteCompat(t *testing.T) {
	for _, text := range []string{"keepalive", "ping", "heartbeat", "keep-alive"} {
		rw := newMemWriter()
		w, _ := ssekit.NewWriter(rw)
		if err := w.Comment(text); err != nil {
			t.Fatal(err)
		}
		if got, want := rw.String(), ": "+text+"\n\n"; got != want {
			t.Fatalf("wire = %q, want %q", got, want)
		}
	}
}

func TestSend_FieldOrderIsIDEventRetryData(t *testing.T) {
	rw := newMemWriter()
	w, _ := ssekit.NewWriter(rw)
	if err := w.Send(ssekit.Event{ID: "1", Name: "e", Retry: 1500 * time.Millisecond, Data: []byte("d")}); err != nil {
		t.Fatal(err)
	}
	if got, want := rw.String(), "id: 1\nevent: e\nretry: 1500\ndata: d\n\n"; got != want {
		t.Fatalf("wire = %q, want %q", got, want)
	}
}

func TestSend_RetryOnlyIsABareFrame(t *testing.T) {
	rw := newMemWriter()
	w, _ := ssekit.NewWriter(rw)
	if err := w.Send(ssekit.Event{Retry: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if got, want := rw.String(), "retry: 2000\n\n"; got != want {
		t.Fatalf("wire = %q, want %q", got, want)
	}
	evs, err := collect(t, strings.NewReader(rw.String()))
	if err != nil || len(evs) != 0 {
		t.Fatalf("a retry-only frame must not dispatch: %+v %v", evs, err)
	}
}

func TestSend_MultilineDataSplits(t *testing.T) {
	rw := newMemWriter()
	w, _ := ssekit.NewWriter(rw)
	_ = w.Send(ssekit.Event{Data: []byte("a\nb\r\nc\rd")})
	if got, want := rw.String(), "data: a\ndata: b\ndata: c\ndata: d\n\n"; got != want {
		t.Fatalf("wire = %q, want %q", got, want)
	}
}

func TestSend_OneFlushPerEvent(t *testing.T) {
	rw := newMemWriter()
	w, _ := ssekit.NewWriter(rw)
	base := rw.flushes // NewWriter's own first flush
	for i := range 5 {
		_ = w.Send(ssekit.Event{Data: []byte(strconv.Itoa(i))})
	}
	_ = w.Comment("c")
	if got := rw.flushes - base; got != 6 {
		t.Fatalf("flushes = %d, want 6", got)
	}
	if base != 1 {
		t.Fatalf("NewWriter flushed %d times, want 1", base)
	}
}

func TestSend_InvalidFieldRejected(t *testing.T) {
	bad := []ssekit.Event{
		{ID: "a\nb"}, {ID: "a\rb"}, {ID: "a\x00b"},
		{Name: "a\nevent: evil"}, {Name: "a\rb"},
		{Retry: -time.Second},
	}
	for _, e := range bad {
		rw := newMemWriter()
		w, _ := ssekit.NewWriter(rw)
		before := rw.String()
		if err := w.Send(e); !errors.Is(err, ssekit.ErrInvalidField) {
			t.Errorf("Send(%+v) = %v, want ErrInvalidField", e, err)
		}
		if rw.String() != before {
			t.Errorf("bytes written for rejected event %+v: %q", e, rw.String())
		}
		// A rejected event does not kill the writer.
		if err := w.Send(ssekit.Event{Data: []byte("ok")}); err != nil {
			t.Errorf("writer unusable after rejection: %v", err)
		}
	}
}

func TestNewWriter_ErrNoFlusherLeavesHeadersUntouched(t *testing.T) {
	nf := &noFlushWriter{hdr: http.Header{}}
	w, err := ssekit.NewWriter(nf)
	if !errors.Is(err, ssekit.ErrNoFlusher) || w != nil {
		t.Fatalf("got (%v, %v)", w, err)
	}
	if len(nf.hdr) != 0 {
		t.Fatalf("headers touched: %v", nf.hdr)
	}
}

type wrapper struct{ http.ResponseWriter }

func (w wrapper) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestNewWriter_FindsFlusherThroughUnwrap(t *testing.T) {
	rw := newMemWriter()
	if _, err := ssekit.NewWriter(wrapper{rw}); err != nil {
		t.Fatal(err)
	}
	if _, err := ssekit.NewWriter(wrapper{&noFlushWriter{hdr: http.Header{}}}); !errors.Is(err, ssekit.ErrNoFlusher) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewWriter_DefaultAndOverriddenHeaders(t *testing.T) {
	rw := newMemWriter()
	if _, err := ssekit.NewWriter(rw); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Content-Type": "text/event-stream", "Cache-Control": "no-cache, no-transform", "X-Accel-Buffering": "no",
	}
	for k, v := range want {
		if got := rw.hdr.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if len(rw.hdr) != len(want) {
		t.Errorf("unexpected extra headers (no CORS, no Connection): %v", rw.hdr)
	}
	if rw.status != http.StatusOK {
		t.Errorf("status = %d", rw.status)
	}

	rw = newMemWriter()
	_, _ = ssekit.NewWriter(rw, ssekit.WithoutBufferingHint(), ssekit.WithCacheControl("no-store"), ssekit.WithHeader("X-Thing", "1"))
	if rw.hdr.Get("X-Accel-Buffering") != "" || rw.hdr.Get("Cache-Control") != "no-store" || rw.hdr.Get("X-Thing") != "1" {
		t.Errorf("options not applied: %v", rw.hdr)
	}
}

func TestSend_WriteAndFlushErrorsAreReturned(t *testing.T) {
	boom := errors.New("client went away")

	rw := newMemWriter()
	w, _ := ssekit.NewWriter(rw)
	rw.mu.Lock()
	rw.failAfter, rw.writeErr = 1, boom
	rw.mu.Unlock()
	if err := w.Send(ssekit.Event{Data: []byte("1")}); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if err := w.Send(ssekit.Event{Data: []byte("2")}); !errors.Is(err, boom) {
		t.Fatalf("second send = %v, want the write error", err)
	}
	// Sticky: a dead writer keeps saying so, for Comment too.
	if err := w.Comment("x"); !errors.Is(err, boom) {
		t.Fatalf("comment after failure = %v", err)
	}

	rw = newMemWriter()
	w, _ = ssekit.NewWriter(rw)
	rw.mu.Lock()
	rw.flushErr = boom
	rw.mu.Unlock()
	if err := w.Send(ssekit.Event{Data: []byte("1")}); !errors.Is(err, boom) {
		t.Fatalf("flush error = %v", err)
	}

	rw = newMemWriter()
	rw.flushErr = boom
	if _, err := ssekit.NewWriter(rw); !errors.Is(err, boom) {
		t.Fatalf("NewWriter must return a failing first flush: %v", err)
	}
}

// A real connection: the peer hangs up and Send starts failing.
func TestSend_ErrorAfterPeerDisconnects(t *testing.T) {
	noLeaks(t)
	errc := make(chan error, 1)
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w, err := ssekit.NewWriter(rw)
		if err != nil {
			errc <- err
			return
		}
		<-r.Context().Done() // the client is gone
		<-hold
		var last error
		big := make([]byte, 64<<10)
		for range 200 { // the first writes after a close may still be buffered
			if last = w.Send(ssekit.Event{Data: big}); last != nil {
				break
			}
		}
		errc <- last
	}))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	srv.CloseClientConnections()
	close(hold)
	if err := <-errc; err == nil {
		t.Fatal("Send never reported the disconnected peer")
	}
}

// The deadline behavior the applications need: a server with short read and
// write timeouts still carries a stream much longer than either.
func TestNewWriter_SurvivesServerTimeouts(t *testing.T) {
	const (
		timeout = 150 * time.Millisecond
		events  = 12
		gap     = 50 * time.Millisecond // 600 ms of stream, four times the timeout
	)
	run := func(t *testing.T, useWriter bool) int {
		noLeaks(t)
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			var send func(i int) error
			if useWriter {
				w, err := ssekit.NewWriter(rw)
				if err != nil {
					return
				}
				send = func(i int) error { return w.Send(ssekit.Event{ID: strconv.Itoa(i), Data: []byte("x")}) }
			} else {
				rw.Header().Set("Content-Type", "text/event-stream")
				rw.WriteHeader(http.StatusOK)
				send = func(i int) error {
					_, err := io.WriteString(rw, "id: "+strconv.Itoa(i)+"\ndata: x\n\n")
					if err == nil {
						err = http.NewResponseController(rw).Flush()
					}
					return err
				}
			}
			for i := 1; i <= events; i++ {
				if send(i) != nil {
					return
				}
				time.Sleep(gap)
			}
		}))
		srv.Config.ReadTimeout = timeout
		srv.Config.WriteTimeout = timeout
		srv.Start()
		defer srv.Close()

		resp, err := srv.Client().Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		n := 0
		for _, err := range ssekit.Read(resp.Body) {
			if err != nil {
				break
			}
			n++
		}
		return n
	}
	t.Run("ssekit writer", func(t *testing.T) {
		if n := run(t, true); n != events {
			t.Fatalf("received %d of %d events: deadlines were not cleared", n, events)
		}
	})
	// Control: prove the server timeouts really do cut a naive handler, so the
	// test above cannot pass vacuously.
	t.Run("control: plain handler is cut", func(t *testing.T) {
		if n := run(t, false); n >= events {
			t.Fatalf("plain handler delivered all %d events; the timeouts are not biting", n)
		}
	})
}
