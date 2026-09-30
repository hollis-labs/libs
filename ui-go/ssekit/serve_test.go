package ssekit_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	ssekit "github.com/hollis-labs/go-ssekit"
)

func newServeWriter(t *testing.T) (*ssekit.Writer, *memWriter) {
	t.Helper()
	rw := newMemWriter()
	w, err := ssekit.NewWriter(rw)
	if err != nil {
		t.Fatal(err)
	}
	return w, rw
}

// synctest.Test fails the test if any goroutine started inside the bubble is
// still alive when the function returns, so every test below doubles as a
// goroutine-leak check for Serve, its source goroutine and its timers.

func TestServe_HeartbeatCadenceWhenQuiet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, rw := newServeWriter(t)
		ctx, cancel := context.WithCancel(t.Context())
		ch := make(chan ssekit.Event)
		done := make(chan error, 1)
		go func() { done <- ssekit.Serve(ctx, w, ssekit.ChanSource(ch)) }()

		time.Sleep(14 * time.Second)
		synctest.Wait()
		if got := rw.String(); got != "" {
			t.Fatalf("heartbeat before 15s: %q", got)
		}
		time.Sleep(time.Second) // 15s
		synctest.Wait()
		if got, want := rw.String(), ": keepalive\n\n"; got != want {
			t.Fatalf("at 15s wire = %q, want %q", got, want)
		}
		time.Sleep(15 * time.Second) // 30s
		synctest.Wait()
		if got, want := rw.String(), ": keepalive\n\n: keepalive\n\n"; got != want {
			t.Fatalf("at 30s wire = %q, want %q", got, want)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve = %v", err)
		}
	})
}

func TestServe_EventsDeferTheHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, rw := newServeWriter(t)
		ch := make(chan ssekit.Event)
		done := make(chan error, 1)
		go func() {
			done <- ssekit.Serve(t.Context(), w, ssekit.ChanSource(ch), ssekit.WithHeartbeat(10*time.Second), ssekit.WithHeartbeatText("ping"))
		}()
		for i := range 3 { // an event every 6s: never quiet for 10s
			time.Sleep(6 * time.Second)
			ch <- ssekit.Event{ID: strconv.Itoa(i), Data: []byte("x")}
			synctest.Wait()
		}
		want := "id: 0\ndata: x\n\nid: 1\ndata: x\n\nid: 2\ndata: x\n\n"
		if got := rw.String(); got != want {
			t.Fatalf("wire = %q, want %q", got, want)
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if got := rw.String(); got != want+": ping\n\n" {
			t.Fatalf("wire = %q", got)
		}
		close(ch)
		if err := <-done; err != nil {
			t.Fatalf("Serve = %v", err)
		}
	})
}

func TestServe_HeartbeatOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, rw := newServeWriter(t)
		ch := make(chan ssekit.Event)
		done := make(chan error, 1)
		go func() { done <- ssekit.Serve(t.Context(), w, ssekit.ChanSource(ch), ssekit.WithHeartbeat(0)) }()
		time.Sleep(time.Hour)
		synctest.Wait()
		if rw.String() != "" {
			t.Fatalf("wire = %q", rw.String())
		}
		close(ch)
		<-done
	})
}

func TestServe_MaxLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, rw := newServeWriter(t)
		ch := make(chan ssekit.Event)
		start := time.Now()
		err := ssekit.Serve(t.Context(), w, ssekit.ChanSource(ch),
			ssekit.WithHeartbeat(20*time.Second), ssekit.WithMaxLifetime(50*time.Second))
		if err != nil {
			t.Fatalf("Serve = %v, want nil (a clean end the client resumes from)", err)
		}
		if el := time.Since(start); el != 50*time.Second {
			t.Fatalf("lifetime = %v", el)
		}
		if got, want := rw.String(), ": keepalive\n\n: keepalive\n\n"; got != want {
			t.Fatalf("wire = %q, want %q", got, want)
		}
	})
}

func TestServe_TerminalThenStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, rw := newServeWriter(t)
		ch := make(chan ssekit.Event, 4)
		ch <- ssekit.Event{Name: "delta", Data: []byte("1")}
		ch <- ssekit.Event{Name: "done", Data: []byte("2")}
		ch <- ssekit.Event{Name: "after", Data: []byte("3")}
		err := ssekit.Serve(t.Context(), w, ssekit.ChanSource(ch),
			ssekit.WithTerminal(func(e ssekit.Event) bool { return e.Name == "done" }))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := rw.String(), "event: delta\ndata: 1\n\nevent: done\ndata: 2\n\n"; got != want {
			t.Fatalf("wire = %q, want %q (terminal delivered, nothing after)", got, want)
		}
	})
}

func TestServe_SourceEOFEndsCleanly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, rw := newServeWriter(t)
		ch := make(chan ssekit.Event, 1)
		ch <- ssekit.Event{Data: []byte("only")}
		close(ch)
		if err := ssekit.Serve(t.Context(), w, ssekit.ChanSource(ch)); err != nil {
			t.Fatal(err)
		}
		if got := rw.String(); got != "data: only\n\n" {
			t.Fatalf("wire = %q", got)
		}
	})
}

func TestServe_ContextCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, _ := newServeWriter(t)
		ctx, cancel := context.WithCancel(t.Context())
		ch := make(chan ssekit.Event)
		done := make(chan error, 1)
		go func() { done <- ssekit.Serve(ctx, w, ssekit.ChanSource(ch)) }()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve = %v", err)
		}
	})
}

// A source that ignores nothing but blocks until its context ends must not
// outlive Serve.
func TestServe_NoGoroutineLeakOnBlockedSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, _ := newServeWriter(t)
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		src := ssekit.SourceFunc(func(ctx context.Context) (ssekit.Event, error) {
			<-ctx.Done()
			return ssekit.Event{}, ctx.Err()
		})
		if err := ssekit.Serve(ctx, w, src); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Serve = %v", err)
		}
	})
}

func TestServe_WriteErrorIsReturned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		boom := errors.New("broken pipe")
		w, rw := newServeWriter(t)
		rw.mu.Lock()
		rw.failAfter, rw.writeErr = 1, boom
		rw.mu.Unlock()
		ch := make(chan ssekit.Event, 3)
		for i := range 3 {
			ch <- ssekit.Event{Data: []byte(strconv.Itoa(i))}
		}
		err := ssekit.Serve(t.Context(), w, ssekit.ChanSource(ch))
		if !errors.Is(err, boom) {
			t.Fatalf("Serve = %v, want the write error so callers can abort upstream work", err)
		}
		if got := rw.String(); got != "data: 0\n\n" {
			t.Fatalf("wire = %q", got)
		}
	})
}

func TestServe_HeartbeatWriteErrorIsReturned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		boom := errors.New("broken pipe")
		w, rw := newServeWriter(t)
		rw.mu.Lock()
		rw.failAfter, rw.writeErr = 0, boom
		rw.mu.Unlock()
		err := ssekit.Serve(t.Context(), w, ssekit.ChanSource(make(chan ssekit.Event)))
		if !errors.Is(err, boom) {
			t.Fatalf("Serve = %v", err)
		}
	})
}

func TestServe_SourceErrorInBandFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, rw := newServeWriter(t)
		bad := errors.New("upstream failed")
		src := ssekit.SourceFunc(func(context.Context) (ssekit.Event, error) { return ssekit.Event{}, bad })
		err := ssekit.Serve(t.Context(), w, src, ssekit.WithOnSourceError(func(e error) (ssekit.Event, bool) {
			return ssekit.Event{Name: "error", Data: []byte(e.Error())}, true
		}))
		if !errors.Is(err, bad) {
			t.Fatalf("Serve = %v", err)
		}
		if got, want := rw.String(), "event: error\ndata: upstream failed\n\n"; got != want {
			t.Fatalf("wire = %q, want %q", got, want)
		}
	})
}

func TestServe_SourceErrorWithoutHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, rw := newServeWriter(t)
		bad := errors.New("upstream failed")
		src := ssekit.SourceFunc(func(context.Context) (ssekit.Event, error) { return ssekit.Event{}, bad })
		if err := ssekit.Serve(t.Context(), w, src); !errors.Is(err, bad) {
			t.Fatalf("Serve = %v", err)
		}
		if rw.String() != "" {
			t.Fatalf("wire = %q", rw.String())
		}
	})
}

// A slow writer must not lose or reorder events: the source is read one event
// ahead, never more.
func TestServe_OrderPreserved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, rw := newServeWriter(t)
		ch := make(chan ssekit.Event)
		go func() {
			for i := range 50 {
				ch <- ssekit.Event{ID: strconv.Itoa(i), Data: []byte("x")}
			}
			close(ch)
		}()
		if err := ssekit.Serve(t.Context(), w, ssekit.ChanSource(ch)); err != nil {
			t.Fatal(err)
		}
		evs, _ := collectBytes(rw.Bytes())
		if len(evs) != 50 {
			t.Fatalf("got %d events", len(evs))
		}
		for i, e := range evs {
			if e.ID != strconv.Itoa(i) {
				t.Fatalf("event %d has id %q", i, e.ID)
			}
		}
	})
}

func collectBytes(b []byte) ([]ssekit.Event, error) {
	var evs []ssekit.Event
	for ev, err := range ssekit.Read(bytesReader(b)) {
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
