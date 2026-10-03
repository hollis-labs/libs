package ssetest_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	ssekit "github.com/hollis-labs/go-ssekit"
	"github.com/hollis-labs/go-ssekit/ssetest"
)

// timedSink notes when each write arrives.
type timedSink struct {
	start time.Time
	at    []time.Duration
	buf   bytes.Buffer
}

func (s *timedSink) Write(p []byte) (int, error) {
	s.at = append(s.at, time.Since(s.start))
	return s.buf.Write(p)
}

func TestRecordReplay_PreservesChunksAndTiming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			for _, step := range []struct {
				wait  time.Duration
				chunk string
			}{
				{0, "id: 1\ndata: a\n\n"},
				{250 * time.Millisecond, "id: 2\nda"},
				{750 * time.Millisecond, "ta: b\n\n"},
				{15 * time.Second, ": keepalive\n\n"},
			} {
				time.Sleep(step.wait)
				_, _ = io.WriteString(pw, step.chunk)
			}
		}()
		frames, err := ssetest.Record(pr)
		if err != nil {
			t.Fatal(err)
		}
		wantAt := []time.Duration{0, 250 * time.Millisecond, time.Second, 16 * time.Second}
		if len(frames) != len(wantAt) {
			t.Fatalf("frames = %d", len(frames))
		}
		for i, f := range frames {
			if f.At != wantAt[i] {
				t.Errorf("frame %d at %v, want %v", i, f.At, wantAt[i])
			}
		}
		if string(frames[1].Raw) != "id: 2\nda" {
			t.Errorf("chunk boundary lost: %q", frames[1].Raw)
		}

		// Replay reproduces the same chunks at the same offsets.
		sink := &timedSink{start: time.Now()}
		if err := ssetest.Replay(t.Context(), sink, frames); err != nil {
			t.Fatal(err)
		}
		for i := range wantAt {
			if sink.at[i] != wantAt[i] {
				t.Errorf("replayed write %d at %v, want %v", i, sink.at[i], wantAt[i])
			}
		}
		if !bytes.Equal(sink.buf.Bytes(), ssetest.Concat(frames)) {
			t.Fatal("replayed bytes differ")
		}

		// And what was recorded parses to the same events.
		var ids []string
		for ev, err := range ssekit.Read(bytes.NewReader(ssetest.Concat(frames))) {
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, ev.ID+string(ev.Data))
		}
		if len(ids) != 2 || ids[0] != "1a" || ids[1] != "2b" {
			t.Fatalf("events = %v", ids)
		}
	})
}

func TestReplay_StopsOnContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		var buf bytes.Buffer
		err := ssetest.Replay(ctx, &buf, []ssetest.Frame{{At: 0, Raw: []byte("a")}, {At: time.Hour, Raw: []byte("b")}})
		if !errors.Is(err, context.DeadlineExceeded) || buf.String() != "a" {
			t.Fatalf("err = %v, wrote %q", err, buf.String())
		}
	})
}

func TestReplay_FlushesHTTPResponseWriters(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := ssetest.Replay(t.Context(), rec, []ssetest.Frame{{Raw: []byte("x")}, {Raw: []byte("y")}}); err != nil {
		t.Fatal(err)
	}
	if !rec.Flushed || rec.Body.String() != "xy" {
		t.Fatalf("flushed=%v body=%q", rec.Flushed, rec.Body.String())
	}
}

func TestReplay_ReturnsWriteError(t *testing.T) {
	boom := errors.New("closed")
	err := ssetest.Replay(t.Context(), failWriter{boom}, []ssetest.Frame{{Raw: []byte("x")}})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

type failWriter struct{ err error }

func (f failWriter) Write([]byte) (int, error) { return 0, f.err }
