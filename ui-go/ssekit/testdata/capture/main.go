// Command capture prints the bytes that copies of the SSE framing statements
// in the applications ssekit was extracted from write to a recorder. It is a
// copy, not the applications: each function below reproduces the fmt calls of
// the function named in its comment (apps/ checkout as read on 2026-09-29) with
// payloads and dependencies stubbed, so what it proves is the framing those
// statements produce, not the applications' behavior. The output is what
// wire_test.go's byte-compat table was built from.
//
// Run: go run ./testdata/capture
package main

import (
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
)

// apps/nanite/internal/api/host_runtime_feed.go, handleHostRuntimeFeed.
func naniteHostFeed(w io.Writer) {
	data := []byte(`{"head":1}`)
	fmt.Fprintf(w, "event: host_runtime.head.v1\ndata: %s\n\n", data)
	fmt.Fprintf(w, "id: %d\nevent: host_runtime.gap.v1\ndata: %s\n\n", 7, data)
	fmt.Fprintf(w, "id: %d\nevent: host_runtime.v1\ndata: %s\n\n", 9, data)
	io.WriteString(w, ": keepalive\n\n")
}

// apps/nanite/internal/api/messages.go, streamMessageEvents.
func naniteMessages(w io.Writer, eventID uint64, typ string) {
	data := []byte(`{"type":"` + typ + `"}`)
	if eventID > 0 {
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", eventID, typ, data)
	} else {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
	}
}

// apps/tangent/internal/server/turns.go, writeTurnsRevisionEvent.
func tangentRevision(w io.Writer, revision string) error {
	payload := []byte(`{"revision":"` + revision + `"}`) // stands in for json.Marshal of a one-key map
	_, err := fmt.Fprintf(w, "event: revision\ndata: %s\n\n", payload)
	return err
}

// apps/tether/internal/api/events.go, writeSSEEvent (buffer logic kept).
func tetherEvent(w io.Writer, seq int64, kind, dataJSON string) {
	var buf strings.Builder
	fmt.Fprintf(&buf, "id: %d\n", seq)
	if kind != "" {
		fmt.Fprintf(&buf, "event: %s\n", kind)
	}
	fmt.Fprintf(&buf, "data: %s\n\n", dataJSON)
	w.Write([]byte(buf.String()))
}

func main() {
	show := func(name string, f func(w io.Writer)) {
		rec := httptest.NewRecorder()
		f(rec)
		fmt.Printf("%s: %q\n", name, rec.Body.String())
	}
	show("nanite host feed", naniteHostFeed)
	show("nanite messages id", func(w io.Writer) { naniteMessages(w, 12, "text_delta") })
	show("nanite messages no id", func(w io.Writer) { naniteMessages(w, 0, "session_takeover") })
	show("tangent revision", func(w io.Writer) { _ = tangentRevision(w, "r1") })
	show("tether with kind", func(w io.Writer) { tetherEvent(w, 42, "session.started", `{"scope":"session"}`) })
	show("tether no kind", func(w io.Writer) { tetherEvent(w, 43, "", `{"scope":"daemon"}`) })
	show("tether ping", func(w io.Writer) { fmt.Fprint(w, ": ping\n\n") })
	show("empty data", func(w io.Writer) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", "x", "") })
}
