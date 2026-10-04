package subprocess

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadFrameBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		limit           int
		failure         error
	}{
		{"LF", "abc\n", "abc", 4, nil}, {"CRLF", "ab\r\n", "ab", 4, nil},
		{"CR counts", "abc\r\n", "", 4, &FrameTooLargeError{}},
		{"truncated", "abc", "", 4, ErrTruncatedFrame}, {"invalid UTF8", "\xff\n", "", 4, ErrFrameUTF8},
		{"no drain", strings.Repeat("x", 100), "", 4, &FrameTooLargeError{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readFrame(bufio.NewReaderSize(strings.NewReader(tc.raw), 16), tc.limit)
			if tc.failure == nil {
				if err != nil || string(got) != tc.want {
					t.Fatalf("%q %v", got, err)
				}
			} else {
				var large *FrameTooLargeError
				if _, ok := tc.failure.(*FrameTooLargeError); ok {
					if !errors.As(err, &large) || large.Direction != "input" || large.Limit != tc.limit {
						t.Fatal(err)
					}
				} else if !errors.Is(err, tc.failure) {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestBoundedFrameEncoding(t *testing.T) {
	for _, value := range []any{"é\n\"", []byte{0, 1, 2}, json.RawMessage("{\n\"x\": 1\n}"), map[string]any{"x": []any{true, nil, 3}}} {
		raw, err := marshalBounded(value, 100)
		if err != nil || !json.Valid(raw) || bytes.Contains(raw, []byte{'\n'}) {
			t.Fatalf("%s %v", raw, err)
		}
		if _, err = marshalBounded(value, len(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err = marshalBounded(value, len(raw)-1); err == nil {
			t.Fatal("accepted oversized encoding")
		}
	}
	s := server{outputLimit: 4}
	raw, err := s.encodeFrame("a")
	if err != nil || string(raw) != "\"a\"\n" {
		t.Fatalf("%q %v", raw, err)
	}
	if _, err = s.encodeFrame("aa"); err == nil {
		t.Fatal("LF excluded from cap")
	}
}

type prefixWriter struct {
	writes int
	data   []byte
}

func (w *prefixWriter) Write(b []byte) (int, error) {
	w.writes++
	w.data = append(w.data, b[:1]...)
	return 1, nil
}
func TestFramePartialWriteFences(t *testing.T) {
	w := &prefixWriter{}
	err := ServeWithOptions(&transcriptBase{}, ServeOptions{Input: strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"plugin/health\"}\n"), Output: w})
	if !errors.Is(err, io.ErrShortWrite) || w.writes != 1 {
		t.Fatalf("%v writes=%d", err, w.writes)
	}
}

type heldFrameWriter struct{ started, release, finished chan struct{} }

func (w *heldFrameWriter) Write(b []byte) (int, error) {
	close(w.started)
	<-w.release
	defer close(w.finished)
	return len(b), nil
}
func TestFrameBlockedWriteWithoutEOF(t *testing.T) {
	inR, inW := io.Pipe()
	defer inR.Close()
	defer inW.Close()
	w := &heldFrameWriter{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	defer func() { close(w.release); <-w.finished }()
	done := make(chan error, 1)
	go func() {
		done <- ServeWithOptions(&transcriptBase{}, ServeOptions{Input: inR, Output: w, WriteTimeout: 20 * time.Millisecond})
	}()
	_, err := io.WriteString(inW, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"plugin/health\"}\n")
	if err != nil {
		t.Fatal(err)
	}
	<-w.started
	select {
	case err := <-done:
		if !errors.Is(err, ErrWriteTimeout) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked write did not fence without EOF")
	}
}
func TestFrameFallbackOrFence(t *testing.T) {
	var written []byte
	var failure error
	s := server{outputLimit: 128, writeFrame: func(b []byte) { written = append(written, b...) }, fence: func(err error) { failure = err }}
	s.writeMessage(RPCResponse{JSONRPC: "2.0", ID: NumberID(1), Result: json.RawMessage(`"` + strings.Repeat("x", 200) + `"`)})
	if failure != nil || !bytes.Contains(written, []byte(`"code":-32603`)) || len(written) > 128 {
		t.Fatalf("%s %v", written, failure)
	}
	written = nil
	s.outputLimit = 8
	s.writeMessage(RPCResponse{JSONRPC: "2.0", ID: NumberID(1)})
	var large *FrameTooLargeError
	if !errors.As(failure, &large) || len(written) != 0 || large.Limit != 8 {
		t.Fatalf("%s %v", written, failure)
	}
}
func TestBoundedStructJSONSemantics(t *testing.T) {
	type Base struct {
		X      string `json:"x"`
		Hidden string `json:"-"`
	}
	type Outer struct {
		Base
		X     string `json:"x"`
		N     int    `json:"n,string"`
		Empty string `json:"empty,omitempty"`
	}
	for _, value := range []any{Outer{Base: Base{X: "hidden"}, X: "selected", N: 3}, struct{ *Base }{nil}, json.Number("")} {
		want, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := marshalBounded(value, 1024)
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		json.Unmarshal(want, &a)
		json.Unmarshal(got, &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("got %s want %s", got, want)
		}
	}
}
func TestFrameServeCannotFitError(t *testing.T) {
	var output bytes.Buffer
	err := ServeWithOptions(&transcriptBase{}, ServeOptions{Input: strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"plugin/health\"}\n"), Output: &output, FrameLimits: FrameLimits{OutputBytes: 8}})
	var large *FrameTooLargeError
	if !errors.As(err, &large) || large.Limit != 8 || output.Len() != 0 {
		t.Fatalf("%v %s", err, output.Bytes())
	}
}

func TestBoundedEncodingDeterministicBytes(t *testing.T) {
	type Inner struct {
		Shadow string `json:"shadow"`
		First  string `json:"first"`
		N      int    `json:"n,string"`
	}
	type Outer struct {
		Start string `json:"start"`
		Inner
		Last   string `json:"last"`
		Shadow string `json:"shadow"`
		Empty  string `json:"empty,omitempty"`
	}
	type Left struct {
		Clash string
		L     int `json:"left"`
	}
	type Right struct {
		Clash string
		R     int `json:"right"`
	}
	type Ambiguous struct {
		Left
		Right
		Tail string `json:"tail"`
	}
	cases := []struct {
		name  string
		value any
	}{
		{"embedded and dominant index", Outer{Start: "a", Inner: Inner{Shadow: "hidden", First: "b", N: 3}, Last: "c", Shadow: "selected"}},
		{"ambiguous embedded fields", Ambiguous{Left: Left{"hidden", 1}, Right: Right{"hidden", 2}, Tail: "end"}},
		{"nil embedded pointer", struct {
			Before int `json:"before"`
			*Inner
			After int `json:"after"`
		}{Before: 1, After: 2}},
		{"string maps", map[string]any{"z": 3, "a": map[string]any{"y": true, "b": "text"}, "m": []any{2, 1}}},
		{"integer maps", map[int]string{2: "two", 10: "ten", -1: "negative", 0: "zero"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 100; i++ {
				got, err := marshalBounded(tc.value, 4096)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("iteration %d: got %s want %s", i, got, want)
				}
			}
		})
	}
	// The allowed HTML/line-separator escaping difference is still repeatable.
	value := map[string]any{"z": "<>&\u2028\u2029", "a": "first"}
	want, err := marshalBounded(value, 4096)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		got, err := marshalBounded(value, 4096)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("iteration %d: %s %v", i, got, err)
		}
	}
}
