package ssekit_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	ssekit "github.com/hollis-labs/go-ssekit"
	"github.com/hollis-labs/go-ssekit/conformance"
)

func collect(t *testing.T, r io.Reader, o ...ssekit.ReadOption) ([]ssekit.Event, error) {
	t.Helper()
	var evs []ssekit.Event
	for ev, err := range ssekit.Read(r, o...) {
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

// The WHATWG vectors live in package conformance so any other parser can run
// them; here they are run against Read itself, whole and chunked (one byte per
// Read and a split at every offset).
func TestRead_WHATWGVectors(t *testing.T) {
	conformance.Run(t, func(r io.Reader) ([]ssekit.Event, error) {
		return collect(t, r)
	})
}

func TestRead_DataIsCallerOwned(t *testing.T) {
	evs, err := collect(t, strings.NewReader("data: aaa\n\ndata: bbb\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	evs[0].Data[0] = 'X'
	if string(evs[1].Data) != "bbb" {
		t.Fatalf("events alias each other: %q", evs[1].Data)
	}
}

func TestRead_OversizeEndsWithErrEventTooLarge(t *testing.T) {
	in := "data: ok\n\ndata: " + strings.Repeat("a", 500) + "\n\ndata: after\n\n"
	evs, err := collect(t, strings.NewReader(in), ssekit.WithMaxEventBytes(100))
	if !errors.Is(err, ssekit.ErrEventTooLarge) {
		t.Fatalf("err = %v, want ErrEventTooLarge", err)
	}
	if len(evs) != 1 || string(evs[0].Data) != "ok" {
		t.Fatalf("events before the oversize block = %+v", evs)
	}
}

// A block that never terminates must be bounded too, not buffered forever.
func TestRead_UnterminatedBlockIsBounded(t *testing.T) {
	endless := &endlessReader{}
	_, err := collect(t, endless, ssekit.WithMaxEventBytes(4096))
	if !errors.Is(err, ssekit.ErrEventTooLarge) {
		t.Fatalf("err = %v, want ErrEventTooLarge", err)
	}
	if endless.n > 1<<20 {
		t.Fatalf("read %d bytes before giving up", endless.n)
	}
}

type endlessReader struct{ n int }

func (e *endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	e.n += len(p)
	return len(p), nil
}

func TestRead_LongLinesAreNotTruncatedAtScannerLimit(t *testing.T) {
	payload := strings.Repeat("x", 200*1024) // bufio.Scanner's default token cap is 64 KiB
	evs, err := collect(t, strings.NewReader("data: "+payload+"\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || string(evs[0].Data) != payload {
		t.Fatalf("long line lost or truncated: %d events", len(evs))
	}
}

func TestRead_ReadErrorIsYieldedOnce(t *testing.T) {
	boom := errors.New("boom")
	r := io.MultiReader(strings.NewReader("data: a\n\ndata: b"), iotest.ErrReader(boom))
	evs, err := collect(t, r)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %+v", evs)
	}
}

func TestRead_StopEarly(t *testing.T) {
	n := 0
	for range ssekit.Read(strings.NewReader("data: a\n\ndata: b\n\ndata: c\n\n")) {
		n++
		break
	}
	if n != 1 {
		t.Fatal(n)
	}
}

// Round trip: whatever Send writes, Read gives back byte for byte.
func TestRoundTrip_WriterToReader(t *testing.T) {
	datas := []string{"", "x", "a\nb", "a\r\nb", "a\rb", "trailing\n", "\n", "\n\nx", "a\r", "  lead", "data: nested\nid: 9"}
	for _, d := range datas {
		t.Run(fmt.Sprintf("%q", d), func(t *testing.T) {
			rw := newMemWriter()
			w, err := ssekit.NewWriter(rw)
			if err != nil {
				t.Fatal(err)
			}
			if err = w.Send(ssekit.Event{ID: "7", Name: "n", Data: []byte(d)}); err != nil {
				t.Fatal(err)
			}
			evs, err := collect(t, bytes.NewReader(rw.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			// A lone CR at the very end is reported by the writer as a line
			// break, so it round-trips as "\n".
			wantData := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(d)
			if len(evs) != 1 || string(evs[0].Data) != wantData || evs[0].ID != "7" || evs[0].Name != "n" {
				t.Fatalf("got %+v for %q (wire %q)", evs, d, rw.Bytes())
			}
		})
	}
}

func FuzzRead(f *testing.F) {
	for _, v := range conformance.Vectors {
		f.Add([]byte(v.In))
	}
	f.Add([]byte("data: a\r\ndata: b\rid: 1\n\n\xef\xbb\xbf: c\n"))
	f.Fuzz(func(t *testing.T, in []byte) {
		whole, errWhole := collect(t, bytes.NewReader(in), ssekit.WithMaxEventBytes(1<<16))
		byByte, errByte := collect(t, iotest.OneByteReader(bytes.NewReader(in)), ssekit.WithMaxEventBytes(1<<16))
		if (errWhole == nil) != (errByte == nil) {
			t.Fatalf("errors differ: %v vs %v", errWhole, errByte)
		}
		if len(whole) != len(byByte) {
			t.Fatalf("chunking changed the event count: %d vs %d", len(whole), len(byByte))
		}
		for i := range whole {
			a, b := whole[i], byByte[i]
			if a.ID != b.ID || a.Name != b.Name || !bytes.Equal(a.Data, b.Data) || a.Retry != b.Retry {
				t.Fatalf("event %d differs: %+v vs %+v", i, a, b)
			}
			if strings.ContainsAny(a.ID, "\x00\r\n") {
				t.Fatalf("id %q contains a forbidden byte", a.ID)
			}
		}
	})
}
