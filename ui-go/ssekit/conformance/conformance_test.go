package conformance

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	ssekit "github.com/hollis-labs/libs/ui-go/ssekit"
)

// fakeT records a fatal and, like *testing.T, stops the calling goroutine.
type fakeT struct {
	failed bool
	msg    string
}

func (f *fakeT) Helper() {}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.failed = true
	f.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// observe runs check on its own goroutine so a fatal's Goexit is contained.
func observe(check func(t tester)) *fakeT {
	f := &fakeT{}
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		check(f)
	}()
	<-fin
	return f
}

// failing returns the names of the Vectors parse fails, using check.
func failing(parse ParseFunc, check func(tester, ParseFunc, Vector)) map[string]string {
	out := map[string]string{}
	for _, v := range Vectors {
		if f := observe(func(t tester) { check(t, parse, v) }); f.failed {
			out[v.Name] = f.msg
		}
	}
	return out
}

func readKit(r io.Reader) ([]ssekit.Event, error) {
	var evs []ssekit.Event
	for ev, err := range ssekit.Read(r) {
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

// quirks switch a deliberately wrong behavior onto an otherwise correct
// second parser. Each mirrors a defect found in a hand-rolled parser in the
// portfolio.
type quirks struct {
	joinNoSeparator bool // agentkit's serve_http_session.go: data lines concatenated, no "\n"
	perLineDispatch bool // go-tether-client messaging_store.go / peerstore.go: one event per data line
	numericID       bool // go-tether-client events.go: strconv.ParseInt on id:, error aborts the stream
	dropRetry       bool // parser that does not surface retry:
	scanLines       bool // bufio.ScanLines: a lone CR never ends a line, an unterminated last line is kept
	dispatchAtEOF   bool // go-sse's behavior: an incomplete event is dispatched at EOF
	singleRead      bool // treats the first Read as the whole stream
	dropSecondChunk bool // loses the data of a second multi-byte Read (a mid-buffer boundary bug)
}

// splitWHATWG splits on LF, CRLF and lone CR, and drops an unterminated final
// line.
func splitWHATWG(data []byte, atEOF bool) (int, []byte, error) {
	i := bytes.IndexAny(data, "\r\n")
	switch {
	case i < 0:
		if atEOF {
			return len(data), nil, nil
		}
		return 0, nil, nil
	case data[i] == '\n':
		return i + 1, data[:i], nil
	case i+1 < len(data):
		if data[i+1] == '\n' {
			return i + 2, data[:i], nil
		}
		return i + 1, data[:i], nil
	case atEOF:
		return i + 1, data[:i], nil
	}
	return 0, nil, nil // a CR at the end of the buffer: wait to see whether an LF follows
}

func (q quirks) parse(r io.Reader) ([]ssekit.Event, error) {
	if q.singleRead {
		buf := make([]byte, 4096)
		n, _ := r.Read(buf)
		r = bytes.NewReader(buf[:n])
	}
	if q.dropSecondChunk {
		r = &dropSecond{r: r}
	}
	sc := bufio.NewScanner(r)
	if !q.scanLines {
		sc.Split(splitWHATWG)
	}
	var (
		evs    []ssekit.Event
		lastID string
		name   string
		retry  time.Duration
		data   []string
		first  = true
	)
	sep := "\n"
	if q.joinNoSeparator {
		sep = ""
	}
	dispatch := func() {
		if len(data) == 0 {
			name = ""
			return
		}
		evs = append(evs, ev(lastID, name, strings.Join(data, sep), retry))
		name, data, retry = "", nil, 0
	}
	for sc.Scan() {
		line := sc.Text()
		if first {
			line = strings.TrimPrefix(line, "\xef\xbb\xbf")
			first = false
		}
		if line == "" {
			dispatch()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data = append(data, value)
			if q.perLineDispatch {
				dispatch()
			}
		case "event":
			name = value
		case "id":
			if q.numericID {
				if _, err := strconv.ParseInt(value, 10, 64); err != nil && value != "" {
					return evs, err
				}
			}
			if !strings.Contains(value, "\x00") {
				lastID = value
			}
		case "retry":
			if q.dropRetry {
				continue
			}
			if ms, err := strconv.ParseUint(value, 10, 63); err == nil && value != "" && !strings.HasPrefix(value, "+") {
				retry = time.Duration(ms) * time.Millisecond
			}
		}
	}
	if q.dispatchAtEOF {
		dispatch()
	}
	return evs, sc.Err()
}

// dropSecond ends the stream at a second Read that follows a multi-byte first
// Read and would itself return more than one byte. One byte per Read and a
// single whole-stream Read never trigger it; a split in the middle does.
type dropSecond struct {
	r     io.Reader
	reads int
	first int
}

func (d *dropSecond) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	d.reads++
	switch {
	case d.reads == 1:
		d.first = n
	case d.reads == 2 && d.first > 1 && n > 1:
		return 0, io.EOF
	}
	return n, err
}

func TestVectorNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range Vectors {
		if seen[v.Name] {
			t.Errorf("duplicate vector name %q", v.Name)
		}
		seen[v.Name] = true
	}
}

// The real parser and an independent reimplementation both pass, whole and
// chunked, through the exported Run.
func TestRun_ConformingParsersPass(t *testing.T) {
	t.Run("ssekit.Read", func(t *testing.T) { Run(t, readKit) })
	t.Run("reference", func(t *testing.T) { Run(t, quirks{}.parse) })
}

// Each defect is caught by the vector that targets it, and that vector passes
// for the correct reference parser, so the failure is attributable.
func TestRun_CatchesKnownDefects(t *testing.T) {
	tests := []struct {
		name   string
		q      quirks
		target string
	}{
		{"agentkit joins multi-line data with no separator (CW-20260930-0052)", quirks{joinNoSeparator: true}, "multi data join"},
		{"tether-client id: ParseInt aborts the stream (CW-20260930-0053)", quirks{numericID: true}, "non-numeric id is opaque"},
		{"per-line dispatch, no folding (CW-20260930-0054)", quirks{perLineDispatch: true}, "multi data join"},
		{"per-line dispatch ignores the blank-line boundary", quirks{perLineDispatch: true}, "mixed endings"},
		{"retry: dropped", quirks{dropRetry: true}, "retry"},
		{"bufio.ScanLines never ends a line at a lone CR", quirks{scanLines: true}, "lone cr"},
		{"incomplete event dispatched at EOF", quirks{dispatchAtEOF: true}, "incomplete event at EOF discarded"},
	}
	good := failing(quirks{}.parse, checkWhole)
	if len(good) != 0 {
		t.Fatalf("reference parser fails vectors: %v", good)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bad := failing(tc.q.parse, checkWhole)
			msg, ok := bad[tc.target]
			if !ok {
				t.Fatalf("vector %q did not catch this defect (failing: %v)", tc.target, keys(bad))
			}
			if !strings.Contains(msg, "want") && !strings.Contains(msg, "parse error") {
				t.Fatalf("failure message does not say what was expected: %q", msg)
			}
		})
	}
}

func TestRun_ParseErrorFails(t *testing.T) {
	boom := errors.New("boom")
	f := observe(func(tt tester) {
		checkWhole(tt, func(io.Reader) ([]ssekit.Event, error) { return nil, boom }, Vectors[0])
	})
	if !f.failed || !strings.Contains(f.msg, "boom") {
		t.Fatalf("parse error not reported: failed=%v msg=%q", f.failed, f.msg)
	}
}

// A parser that only works when the stream arrives in one piece passes every
// whole-stream check and fails the chunking invariant.
func TestRun_CatchesChunkSensitivity(t *testing.T) {
	q := quirks{singleRead: true}
	if bad := failing(q.parse, checkWhole); len(bad) != 0 {
		t.Fatalf("single-read parser fails whole-stream vectors: %v", keys(bad))
	}
	bad := failing(q.parse, checkChunked)
	if _, ok := bad["lf"]; !ok {
		t.Fatalf("chunking invariant did not catch a single-read parser (failing: %v)", keys(bad))
	}
	if !strings.Contains(bad["lf"], "one byte per Read") {
		t.Fatalf("failure does not say how the stream was chunked: %q", bad["lf"])
	}
}

// The two-chunk split at every offset is a separate pass from the
// byte-at-a-time one: it catches a parser that mishandles a boundary in the
// middle of a buffer but copes with one byte per Read.
func TestRun_CatchesMidBufferBoundary(t *testing.T) {
	q := quirks{dropSecondChunk: true}
	if bad := failing(q.parse, checkWhole); len(bad) != 0 {
		t.Fatalf("fails whole-stream vectors: %v", keys(bad))
	}
	bad := failing(q.parse, checkChunked)
	msg, ok := bad["multi data join"]
	if !ok {
		t.Fatalf("chunked check did not catch a mid-buffer boundary bug (failing: %v)", keys(bad))
	}
	if !strings.Contains(msg, "split at byte") {
		t.Fatalf("failure does not name the split: %q", msg)
	}
}

func TestCheckStream_ReportsCountAndFieldMismatch(t *testing.T) {
	want := []ssekit.Event{ev("1", "a", "x", 0)}
	tests := []struct {
		name string
		got  []ssekit.Event
		msg  string
	}{
		{"extra event", []ssekit.Event{ev("1", "a", "x", 0), ev("1", "a", "x", 0)}, "got 2 events"},
		{"no events", nil, "got 0 events"},
		{"id", []ssekit.Event{ev("2", "a", "x", 0)}, "event 0"},
		{"name", []ssekit.Event{ev("1", "b", "x", 0)}, "event 0"},
		{"data", []ssekit.Event{ev("1", "a", "y", 0)}, "event 0"},
		{"retry", []ssekit.Event{ev("1", "a", "x", time.Second)}, "event 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := observe(func(tt tester) {
				checkStream(tt, func(io.Reader) ([]ssekit.Event, error) { return tc.got, nil }, strings.NewReader(""), want, "x")
			})
			if !f.failed || !strings.Contains(f.msg, tc.msg) {
				t.Fatalf("failed=%v msg=%q, want a failure mentioning %q", f.failed, f.msg, tc.msg)
			}
		})
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
