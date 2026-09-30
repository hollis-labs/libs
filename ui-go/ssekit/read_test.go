package ssekit_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	ssekit "github.com/hollis-labs/go-ssekit"
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

type want struct {
	id, name, data string
	retry          time.Duration
}

// The WHATWG vectors. Each one is run whole, one byte per Read, and split at
// every possible offset (TestRead_ChunkingInvariant).
var vectors = []struct {
	name string
	in   string
	want []want
}{
	{"lf", "id: 1\nevent: a\ndata: x\n\n", []want{{"1", "a", "x", 0}}},
	{"crlf", "id: 1\r\nevent: a\r\ndata: x\r\n\r\n", []want{{"1", "a", "x", 0}}},
	{"lone cr", "id: 1\revent: a\rdata: x\r\r", []want{{"1", "a", "x", 0}}},
	{"mixed endings", "data: a\r\ndata: b\rdata: c\n\n", []want{{"", "", "a\nb\nc", 0}}},
	{"cr then lf across the dispatch", "data: x\r\n\r\ndata: y\n\n", []want{{"", "", "x", 0}, {"", "", "y", 0}}},
	{"bom at start", "\xef\xbb\xbfdata: x\n\n", []want{{"", "", "x", 0}}},
	{"bom not at start", "data: a\n\n\xef\xbb\xbfdata: b\n\ndata: c\n\n", []want{{"", "", "a", 0}, {"", "", "c", 0}}},
	{"comment", ": hi\n\ndata: x\n: mid\n\n", []want{{"", "", "x", 0}}},
	{"data without space", "data:x\n\n", []want{{"", "", "x", 0}}},
	{"data one leading space stripped", "data:  x\n\n", []want{{"", "", " x", 0}}},
	{"empty data value dispatches", "data:\n\n", []want{{"", "", "", 0}}},
	{"empty data with space dispatches", "data: \n\n", []want{{"", "", "", 0}}},
	{"bare data field dispatches", "data\n\n", []want{{"", "", "", 0}}},
	{"multi data join", "data: a\ndata: b\n\n", []want{{"", "", "a\nb", 0}}},
	{"multi data with empty middle", "data: a\ndata:\ndata: b\n\n", []want{{"", "", "a\n\nb", 0}}},
	{"no data no dispatch", "event: a\nid: 3\n\ndata: x\n\n", []want{{"3", "", "x", 0}}},
	{"event name resets after dispatch", "event: a\ndata: 1\n\ndata: 2\n\n", []want{{"", "a", "1", 0}, {"", "", "2", 0}}},
	{"event name discarded by a data-less block", "event: a\n\ndata: 2\n\n", []want{{"", "", "2", 0}}},
	{"id persists", "id: 1\ndata: a\n\ndata: b\n\n", []want{{"1", "", "a", 0}, {"1", "", "b", 0}}},
	{"id with NUL ignored", "id: 1\ndata: a\n\nid: a\x00b\ndata: b\n\n", []want{{"1", "", "a", 0}, {"1", "", "b", 0}}},
	{"empty id resets", "id: 1\ndata: a\n\nid:\ndata: b\n\n", []want{{"1", "", "a", 0}, {"", "", "b", 0}}},
	{"bare id resets", "id: 1\ndata: a\n\nid\ndata: b\n\n", []want{{"1", "", "a", 0}, {"", "", "b", 0}}},
	{"retry", "retry: 1500\ndata: x\n\n", []want{{"", "", "x", 1500 * time.Millisecond}}},
	{"retry on its own block rides the next event", "retry: 250\n\ndata: x\n\ndata: y\n\n", []want{{"", "", "x", 250 * time.Millisecond}, {"", "", "y", 0}}},
	{"retry non-digit ignored", "retry: 12a\nretry: -5\nretry:\nretry: 1.5\ndata: x\n\n", []want{{"", "", "x", 0}}},
	{"retry last valid wins", "retry: 10\nretry: 20\nretry: zz\ndata: x\n\n", []want{{"", "", "x", 20 * time.Millisecond}}},
	{"unknown fields ignored", "foo: bar\nbaz\ndata: x\n\n", []want{{"", "", "x", 0}}},
	{"field names are case sensitive", "DATA: no\nData: no\ndata: yes\n\n", []want{{"", "", "yes", 0}}},
	{"colon in value", "data: a:b: c\n\n", []want{{"", "", "a:b: c", 0}}},
	{"many blank lines", "\n\n\ndata: x\n\n\n\n", []want{{"", "", "x", 0}}},
	{"incomplete event at EOF discarded", "data: a\n\ndata: b\n", []want{{"", "", "a", 0}}},
	{"incomplete last line discarded", "data: a\n\ndata: b", []want{{"", "", "a", 0}}},
	{"cr at EOF completes the line but not the event", "data: a\n\ndata: b\r", []want{{"", "", "a", 0}}},
	{"empty stream", "", nil},
	{"binary-ish payload kept opaque", "data: \xff\xfe\n\n", []want{{"", "", "\xff\xfe", 0}}},
}

func check(t *testing.T, got []ssekit.Event, w []want) {
	t.Helper()
	if len(got) != len(w) {
		t.Fatalf("got %d events %+v, want %d %+v", len(got), got, len(w), w)
	}
	for i := range w {
		g := got[i]
		if g.ID != w[i].id || g.Name != w[i].name || string(g.Data) != w[i].data || g.Retry != w[i].retry {
			t.Errorf("event %d = {id:%q name:%q data:%q retry:%v}, want %+v", i, g.ID, g.Name, g.Data, g.Retry, w[i])
		}
	}
}

func TestRead_WHATWGVectors(t *testing.T) {
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			got, err := collect(t, strings.NewReader(v.in))
			if err != nil {
				t.Fatal(err)
			}
			check(t, got, v.want)
		})
	}
}

// Chunk boundaries must not change what is parsed: one byte per Read, and a
// two-chunk split at every offset of every vector.
func TestRead_ChunkingInvariant(t *testing.T) {
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			got, err := collect(t, iotest.OneByteReader(strings.NewReader(v.in)))
			if err != nil {
				t.Fatal(err)
			}
			check(t, got, v.want)
			for cut := 0; cut <= len(v.in); cut++ {
				r := io.MultiReader(strings.NewReader(v.in[:cut]), strings.NewReader(v.in[cut:]))
				got, err := collect(t, r)
				if err != nil {
					t.Fatalf("cut %d: %v", cut, err)
				}
				check(t, got, v.want)
			}
		})
	}
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
	for _, v := range vectors {
		f.Add([]byte(v.in))
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
