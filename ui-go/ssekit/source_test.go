package ssekit_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	ssekit "github.com/hollis-labs/libs/ui-go/ssekit"
)

func drain(ctx context.Context, t *testing.T, s ssekit.Source) ([]string, error) {
	t.Helper()
	var got []string
	for {
		ev, err := s.Next(ctx)
		if err != nil {
			return got, err
		}
		got = append(got, string(ev.Data))
	}
}

func TestChanSource(t *testing.T) {
	ch := make(chan ssekit.Event, 2)
	ch <- ssekit.Event{Data: []byte("a")}
	ch <- ssekit.Event{Data: []byte("b")}
	close(ch)
	got, err := drain(t.Context(), t, ssekit.ChanSource(ch))
	if err != io.EOF || !slices.Equal(got, []string{"a", "b"}) { //nolint:errorlint // sentinel returned unwrapped
		t.Fatalf("got %v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ssekit.ChanSource(make(chan ssekit.Event)).Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestMerge_FanInLiveAndEOF(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mk := func(prefix string, n int) ssekit.Source {
			ch := make(chan ssekit.Event)
			go func() {
				defer close(ch)
				for i := range n {
					ch <- ssekit.Event{Data: []byte(prefix + strconv.Itoa(i))}
				}
			}()
			return ssekit.ChanSource(ch)
		}
		got, err := drain(t.Context(), t, ssekit.Merge(mk("p", 3), mk("q", 4), mk("r", 0)))
		if !errors.Is(err, io.EOF) {
			t.Fatalf("err = %v", err)
		}
		slices.Sort(got)
		if want := []string{"p0", "p1", "p2", "q0", "q1", "q2", "q3"}; !slices.Equal(got, want) {
			t.Fatalf("got %v", got)
		}
	})
}

func TestMerge_ErrorFromAnInputEndsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bad := errors.New("input broke")
		quiet := ssekit.SourceFunc(func(ctx context.Context) (ssekit.Event, error) {
			<-ctx.Done()
			return ssekit.Event{}, ctx.Err()
		})
		broken := ssekit.SourceFunc(func(context.Context) (ssekit.Event, error) { return ssekit.Event{}, bad })
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		_, err := ssekit.Merge(quiet, broken).Next(ctx)
		if !errors.Is(err, bad) {
			t.Fatalf("err = %v", err)
		}
		cancel() // the quiet input's goroutine ends with the context; synctest verifies
	})
}

func TestMerge_ServeNoLeak(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, rw := newServeWriter(t)
		a, b := make(chan ssekit.Event), make(chan ssekit.Event)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- ssekit.Serve(ctx, w, ssekit.Merge(ssekit.ChanSource(a), ssekit.ChanSource(b))) }()
		a <- ssekit.Event{Name: "presence", Data: []byte("1")}
		b <- ssekit.Event{Name: "plugin", Data: []byte("2")}
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if got := rw.String(); got != "event: presence\ndata: 1\n\nevent: plugin\ndata: 2\n\n" {
			t.Fatalf("wire = %q", got)
		}
	})
}

func TestPollSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var cursors []string
		var calls int
		poll := func(_ context.Context, cursor string) ([]ssekit.Event, string, error) {
			mu.Lock()
			defer mu.Unlock()
			cursors = append(cursors, cursor)
			calls++
			switch calls {
			case 1: // first poll is immediate and returns a batch
				return []ssekit.Event{{Data: []byte("a")}, {Data: []byte("b")}}, "c1", nil
			case 2, 3: // nothing new: waits `every` between polls
				return nil, cursor, nil
			case 4:
				return []ssekit.Event{{Data: []byte("c")}}, "c4", nil
			}
			return nil, "", io.EOF
		}
		start := time.Now()
		got, err := drain(t.Context(), t, ssekit.PollSource(poll, 5*time.Second))
		if !errors.Is(err, io.EOF) || !slices.Equal(got, []string{"a", "b", "c"}) {
			t.Fatalf("got %v, %v", got, err)
		}
		if want := []string{"", "c1", "c1", "c1", "c4"}; !slices.Equal(cursors, want) {
			t.Fatalf("cursors = %q, want %q", cursors, want)
		}
		if el := time.Since(start); el != 10*time.Second {
			t.Fatalf("elapsed = %v, want 2 empty polls x 5s", el)
		}
	})
}

func TestPollSource_ErrorAndCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bad := errors.New("db down")
		src := ssekit.PollSource(func(context.Context, string) ([]ssekit.Event, string, error) { return nil, "", bad }, time.Second)
		if _, err := src.Next(t.Context()); !errors.Is(err, bad) {
			t.Fatalf("err = %v", err)
		}
		idle := ssekit.PollSource(func(_ context.Context, c string) ([]ssekit.Event, string, error) { return nil, c, nil }, time.Second)
		ctx, cancel := context.WithTimeout(t.Context(), 2500*time.Millisecond)
		defer cancel()
		if _, err := idle.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
	})
}
