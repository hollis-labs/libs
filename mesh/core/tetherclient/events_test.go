package tether

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStreamEventsToleratesANonNumericID(t *testing.T) {
	flushed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
		}
		_, _ = fmt.Fprint(w, "id: opaque-token\nevent: session.state_changed\ndata: {\"scope\":\"session\",\"session_id\":\"s1\"}\n\n")
		_, _ = fmt.Fprint(w, "id: 7\nevent: broker.envelope_created\ndata: {\"scope\":\"broker\"}\n\n")
		flusher.Flush()
		close(flushed)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	events, errs := MustNew(srv.URL).StreamEvents(ctx, StreamEventsOptions{})
	<-flushed

	first := <-events
	second := <-events
	cancel()

	if first.Seq != 0 || first.Kind != "session.state_changed" || first.SessionID != "s1" {
		t.Fatalf("event with a non-numeric id = %+v, want it delivered with Seq 0", first)
	}
	if second.Seq != 7 || second.Kind != "broker.envelope_created" {
		t.Fatalf("event after a non-numeric id = %+v, want the stream to continue", second)
	}
	select {
	case err := <-errs:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("stream error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not exit after cancellation")
	}
}
