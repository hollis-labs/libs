package sink_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/sink"
	"github.com/hollis-labs/go-chatstream/sink/native"
	"github.com/hollis-labs/go-chatstream/sink/sinktest"
	ssekit "github.com/hollis-labs/go-ssekit"
)

func TestStartAppliesTheEncodersHeadersAndStreams(t *testing.T) {
	rec := httptest.NewRecorder()
	enc := native.New()
	w, err := sink.Start(rec, enc)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	for k, want := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache, no-transform", "X-Accel-Buffering": "no"} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	for _, ev := range sinktest.Named(t, "plain_text").Events {
		if err := enc.Encode(w, ev); err != nil {
			t.Fatal(err)
		}
	}
	if !rec.Flushed {
		t.Error("the response was never flushed")
	}
	frames := sinktest.Parse(t, rec.Body.Bytes())
	if len(frames) == 0 || frames[0].Event != "run.start" || frames[0].ID != "1" {
		t.Errorf("frames = %+v", frames)
	}
}

type noFlush struct {
	h    http.Header
	code int
}

func (n *noFlush) Header() http.Header         { return n.h }
func (n *noFlush) Write(p []byte) (int, error) { return len(p), nil }
func (n *noFlush) WriteHeader(c int)           { n.code = c }

func TestStartRefusesAWriterThatCannotFlushWithoutTouchingIt(t *testing.T) {
	w := &noFlush{h: http.Header{}}
	_, err := sink.Start(w, native.New())
	if !errors.Is(err, ssekit.ErrNoFlusher) {
		t.Fatalf("err = %v", err)
	}
	if len(w.h) != 0 || w.code != 0 {
		t.Errorf("the response was touched: headers %v status %d", w.h, w.code)
	}
}

type failFlush struct{ *httptest.ResponseRecorder }

func (failFlush) FlushError() error { return errors.New("flush refused") }

func TestStartReturnsTheFirstFlushError(t *testing.T) {
	_, err := sink.Start(failFlush{httptest.NewRecorder()}, native.New())
	if err == nil || !strings.Contains(err.Error(), "flush refused") {
		t.Fatalf("err = %v", err)
	}
}

func TestLifecycle(t *testing.T) {
	var l sink.Lifecycle
	if err := l.Admit(); err != nil {
		t.Fatal(err)
	}
	l.SetTerminal()
	if err := l.Admit(); !errors.Is(err, chatstream.ErrAfterTerminal) {
		t.Fatalf("after terminal: %v", err)
	}
	if already, ended := l.Closing(); already || !ended {
		t.Errorf("first Closing = %v, %v", already, ended)
	}
	if already, _ := l.Closing(); !already {
		t.Error("second Closing must report already")
	}
	if err := l.Admit(); !errors.Is(err, sink.ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestMetaHelpers(t *testing.T) {
	ev := chatstream.Event{Meta: map[string]json.RawMessage{
		"s": json.RawMessage(`"text"`), "n": json.RawMessage(`5`), "b": json.RawMessage(`true`), "f": json.RawMessage(`false`),
	}}
	if sink.MetaString(ev, "s") != "text" || sink.MetaString(ev, "n") != "" || sink.MetaString(ev, "missing") != "" {
		t.Error("MetaString")
	}
	if !sink.MetaBool(ev, "b") || sink.MetaBool(ev, "f") || sink.MetaBool(ev, "n") || sink.MetaBool(ev, "missing") {
		t.Error("MetaBool")
	}
	if sink.FrameID(chatstream.Event{}) != "" || sink.FrameID(chatstream.Event{Seq: 42}) != "42" {
		t.Error("FrameID")
	}
	if sink.Milliseconds(time.Time{}) != 0 || sink.Milliseconds(sinktest.T0) != sinktest.T0.UnixMilli() {
		t.Error("Milliseconds")
	}
	if !strings.Contains(sink.CauseText(errors.New("x")), "x") || sink.CauseText(nil) == "" {
		t.Error("CauseText")
	}
}
