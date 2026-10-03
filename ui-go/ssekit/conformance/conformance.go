package conformance

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	ssekit "github.com/hollis-labs/libs/ui-go/ssekit"
)

// Vector is one raw byte stream and the events a conforming parser
// dispatches for it, in order. Only ID, Name, Data and Retry are compared.
//
// ID is the stream's current last-event-ID as the WHATWG algorithm defines
// it: it persists from one event to the next, so an event without its own
// id: field carries the previous one. Name is "" for an event with no (or an
// empty) event: field, not "message". Retry is the most recent valid retry:
// value seen since the previous event, or 0.
type Vector struct {
	Name string
	In   string
	Want []ssekit.Event
}

func ev(id, name, data string, retry time.Duration) ssekit.Event {
	return ssekit.Event{ID: id, Name: name, Data: []byte(data), Retry: retry}
}

// Vectors is the WHATWG raw-frame vector table -- exported so an
// implementation whose own shape doesn't fit ParseFunc can still drive its
// own comparison loop against the same ground truth. Treat it as read-only.
var Vectors = []Vector{
	{"lf", "id: 1\nevent: a\ndata: x\n\n", []ssekit.Event{ev("1", "a", "x", 0)}},
	{"crlf", "id: 1\r\nevent: a\r\ndata: x\r\n\r\n", []ssekit.Event{ev("1", "a", "x", 0)}},
	{"lone cr", "id: 1\revent: a\rdata: x\r\r", []ssekit.Event{ev("1", "a", "x", 0)}},
	{"mixed endings", "data: a\r\ndata: b\rdata: c\n\n", []ssekit.Event{ev("", "", "a\nb\nc", 0)}},
	{"cr then lf across the dispatch", "data: x\r\n\r\ndata: y\n\n", []ssekit.Event{ev("", "", "x", 0), ev("", "", "y", 0)}},
	{"bom at start", "\xef\xbb\xbfdata: x\n\n", []ssekit.Event{ev("", "", "x", 0)}},
	{"bom not at start", "data: a\n\n\xef\xbb\xbfdata: b\n\ndata: c\n\n", []ssekit.Event{ev("", "", "a", 0), ev("", "", "c", 0)}},
	{"comment", ": hi\n\ndata: x\n: mid\n\n", []ssekit.Event{ev("", "", "x", 0)}},
	{"data without space", "data:x\n\n", []ssekit.Event{ev("", "", "x", 0)}},
	{"data one leading space stripped", "data:  x\n\n", []ssekit.Event{ev("", "", " x", 0)}},
	{"empty data value dispatches", "data:\n\n", []ssekit.Event{ev("", "", "", 0)}},
	{"empty data with space dispatches", "data: \n\n", []ssekit.Event{ev("", "", "", 0)}},
	{"bare data field dispatches", "data\n\n", []ssekit.Event{ev("", "", "", 0)}},
	{"multi data join", "data: a\ndata: b\n\n", []ssekit.Event{ev("", "", "a\nb", 0)}},
	{"multi data with empty middle", "data: a\ndata:\ndata: b\n\n", []ssekit.Event{ev("", "", "a\n\nb", 0)}},
	{"no data no dispatch", "event: a\nid: 3\n\ndata: x\n\n", []ssekit.Event{ev("3", "", "x", 0)}},
	{"event name resets after dispatch", "event: a\ndata: 1\n\ndata: 2\n\n", []ssekit.Event{ev("", "a", "1", 0), ev("", "", "2", 0)}},
	{"event name discarded by a data-less block", "event: a\n\ndata: 2\n\n", []ssekit.Event{ev("", "", "2", 0)}},
	{"id persists", "id: 1\ndata: a\n\ndata: b\n\n", []ssekit.Event{ev("1", "", "a", 0), ev("1", "", "b", 0)}},
	{"non-numeric id is opaque", "id: evt-42\ndata: a\n\nid: 7f3c\ndata: b\n\n", []ssekit.Event{ev("evt-42", "", "a", 0), ev("7f3c", "", "b", 0)}},
	{"id with NUL ignored", "id: 1\ndata: a\n\nid: a\x00b\ndata: b\n\n", []ssekit.Event{ev("1", "", "a", 0), ev("1", "", "b", 0)}},
	{"empty id resets", "id: 1\ndata: a\n\nid:\ndata: b\n\n", []ssekit.Event{ev("1", "", "a", 0), ev("", "", "b", 0)}},
	{"bare id resets", "id: 1\ndata: a\n\nid\ndata: b\n\n", []ssekit.Event{ev("1", "", "a", 0), ev("", "", "b", 0)}},
	{"retry", "retry: 1500\ndata: x\n\n", []ssekit.Event{ev("", "", "x", 1500*time.Millisecond)}},
	{"retry on its own block rides the next event", "retry: 250\n\ndata: x\n\ndata: y\n\n", []ssekit.Event{ev("", "", "x", 250*time.Millisecond), ev("", "", "y", 0)}},
	{"retry non-digit ignored", "retry: 12a\nretry: -5\nretry:\nretry: 1.5\ndata: x\n\n", []ssekit.Event{ev("", "", "x", 0)}},
	{"retry last valid wins", "retry: 10\nretry: 20\nretry: zz\ndata: x\n\n", []ssekit.Event{ev("", "", "x", 20*time.Millisecond)}},
	{"unknown fields ignored", "foo: bar\nbaz\ndata: x\n\n", []ssekit.Event{ev("", "", "x", 0)}},
	{"field names are case sensitive", "DATA: no\nData: no\ndata: yes\n\n", []ssekit.Event{ev("", "", "yes", 0)}},
	{"colon in value", "data: a:b: c\n\n", []ssekit.Event{ev("", "", "a:b: c", 0)}},
	{"many blank lines", "\n\n\ndata: x\n\n\n\n", []ssekit.Event{ev("", "", "x", 0)}},
	{"incomplete event at EOF discarded", "data: a\n\ndata: b\n", []ssekit.Event{ev("", "", "a", 0)}},
	{"incomplete last line discarded", "data: a\n\ndata: b", []ssekit.Event{ev("", "", "a", 0)}},
	{"cr at EOF completes the line but not the event", "data: a\n\ndata: b\r", []ssekit.Event{ev("", "", "a", 0)}},
	{"empty stream", "", nil},
	{"binary-ish payload kept opaque", "data: \xff\xfe\n\n", []ssekit.Event{ev("", "", "\xff\xfe", 0)}},
}

// ParseFunc parses one raw SSE byte stream and returns every event it
// dispatches, in order. A parse error on any Vector is a failure: every
// Vector is well-formed input.
type ParseFunc func(r io.Reader) ([]ssekit.Event, error)

// tester is the slice of *testing.T the checks use. Keeping it small lets
// the package's own tests observe a failure without failing themselves.
type tester interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Run runs Vectors against parse, whole, as t.Run subtests named after each
// Vector; then a "chunking invariant" subtest that re-runs every Vector one
// byte per Read and split in two at every byte offset. Chunk boundaries must
// not change what is parsed.
func Run(t *testing.T, parse ParseFunc) {
	t.Helper()
	if parse == nil {
		t.Fatal("conformance.Run: nil ParseFunc")
	}
	for _, v := range Vectors {
		t.Run(v.Name, func(t *testing.T) {
			checkWhole(t, parse, v)
		})
	}
	t.Run("chunking invariant", func(t *testing.T) {
		for _, v := range Vectors {
			t.Run(v.Name, func(t *testing.T) {
				checkChunked(t, parse, v)
			})
		}
	})
}

func checkWhole(t tester, parse ParseFunc, v Vector) {
	t.Helper()
	checkStream(t, parse, strings.NewReader(v.In), v.Want, "whole stream")
}

func checkChunked(t tester, parse ParseFunc, v Vector) {
	t.Helper()
	checkStream(t, parse, iotest.OneByteReader(strings.NewReader(v.In)), v.Want, "one byte per Read")
	for cut := 0; cut <= len(v.In); cut++ {
		r := io.MultiReader(strings.NewReader(v.In[:cut]), strings.NewReader(v.In[cut:]))
		checkStream(t, parse, r, v.Want, fmt.Sprintf("split at byte %d", cut))
	}
}

func checkStream(t tester, parse ParseFunc, r io.Reader, want []ssekit.Event, how string) {
	t.Helper()
	got, err := parse(r)
	if err != nil {
		t.Fatalf("%s: parse error: %v", how, err)
	}
	if len(got) != len(want) {
		t.Fatalf("%s: got %d events %s, want %d %s", how, len(got), show(got), len(want), show(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.ID != w.ID || g.Name != w.Name || string(g.Data) != string(w.Data) || g.Retry != w.Retry {
			t.Fatalf("%s: event %d = %s, want %s", how, i, show1(g), show1(w))
		}
	}
}

func show1(e ssekit.Event) string {
	return fmt.Sprintf("{id:%q name:%q data:%q retry:%v}", e.ID, e.Name, e.Data, e.Retry)
}

func show(evs []ssekit.Event) string {
	parts := make([]string, len(evs))
	for i, e := range evs {
		parts[i] = show1(e)
	}
	return "[" + strings.Join(parts, " ") + "]"
}
