package httpstream_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/hubbind"
	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
	"github.com/hollis-labs/substrate/agent/transport/httpstream"
)

func TestStrictCursorRefusals(t *testing.T) {
	for _, values := range [][]string{{""}, {"+2"}, {"-1"}, {"2", "3"}, {"18446744073709551615"}, {"18446744073709551616"}} {
		r := httptest.NewRequest("GET", "/events", nil)
		for _, v := range values {
			r.Header.Add("Last-Event-ID", v)
		}
		if _, err := httpstream.ParseCursor(r); err == nil {
			t.Fatalf("accepted cursor %v", values)
		}
	}
	r := httptest.NewRequest("GET", "/events?after=3", nil)
	if _, err := httpstream.ParseCursor(r); err == nil {
		t.Fatal("query fallback accepted")
	}
	r = httptest.NewRequest("GET", "/events", nil)
	r.Header.Set("Last-Event-ID", "3")
	if after, err := httpstream.ParseCursor(r); err != nil || after != 3 {
		t.Fatalf("after=%d err=%v", after, err)
	}
}

func TestForeignRunRefusedBeforeEventsAreWritten(t *testing.T) {
	hub := streamhub.New(streamhub.NewMemoryLog(), streamhub.WithAutoOpen(false), streamhub.WithTerminal(hubbind.Terminal))
	if err := hub.Open(t.Context(), "foreign"); err != nil {
		t.Fatal(err)
	}
	_, err := hubbind.Publish(t.Context(), hub, "foreign", chatstream.Event{V: chatstream.SchemaVersion, RunID: "foreign", Time: time.Now().UTC(), Verb: chatstream.VerbRunStart})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := hub.Subscribe(t.Context(), "foreign", streamhub.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	err = httpstream.Write(w, httptest.NewRequest("GET", "/events", nil), sub, httpstream.State{RunID: "expected"}, false)
	if !errors.Is(err, httpstream.ErrForeignRun) || strings.Contains(w.Body.String(), "foreign") {
		t.Fatalf("err=%v body=%s", err, w.Body.String())
	}
}

func TestExpiredReplayProducesGapAndNoOutcome(t *testing.T) {
	w := httptest.NewRecorder()
	err := httpstream.Write(w, httptest.NewRequest("GET", "/events", nil), nil, httpstream.State{RunID: "turn", After: 3, Checkpoint: 10}, true)
	if err != nil || !strings.Contains(w.Body.String(), "gap") || strings.Contains(w.Body.String(), "run.finish") {
		t.Fatalf("err=%v body=%s", err, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "private, no-store, no-transform" || w.Header().Get("X-Chat-Encoding") != "chatstream/v1" {
		t.Fatal(w.Header())
	}
}

func TestDisconnectCannotGenerateTerminal(t *testing.T) {
	hub := streamhub.New(streamhub.NewMemoryLog(), streamhub.WithAutoOpen(false), streamhub.WithTerminal(hubbind.Terminal))
	if err := hub.Open(t.Context(), "turn"); err != nil {
		t.Fatal(err)
	}
	sub, err := hub.Subscribe(t.Context(), "turn", streamhub.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := httptest.NewRequest("GET", "/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	if err := httpstream.Write(w, r, sub, httpstream.State{RunID: "turn"}, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "run.finish") || strings.Contains(w.Body.String(), "run.error") || strings.Contains(w.Body.String(), "run.abort") {
		t.Fatal(w.Body.String())
	}
}

type failedGapWriter struct {
	header http.Header
	cause  error
}

func (w *failedGapWriter) Header() http.Header       { return w.header }
func (w *failedGapWriter) WriteHeader(int)           {}
func (w *failedGapWriter) Flush()                    {}
func (w *failedGapWriter) Write([]byte) (int, error) { return 0, w.cause }

func TestExpiredReplayReportsGapWriteFailure(t *testing.T) {
	cause := errors.New("caller disconnected while writing gap")
	w := &failedGapWriter{header: make(http.Header), cause: cause}
	err := httpstream.Write(w, httptest.NewRequest("GET", "/events", nil), nil, httpstream.State{RunID: "turn", After: 3, Checkpoint: 10}, true)
	if !errors.Is(err, cause) {
		t.Fatalf("lost gap write error: %v", err)
	}
}
