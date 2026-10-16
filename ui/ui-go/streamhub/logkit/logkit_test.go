package logkit_test

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/hollis-labs/libs/ui/ui-go/streamhub"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/logkit"
)

func TestCursorRoundTrip(t *testing.T) {
	e, s, err := logkit.ParseCursor(logkit.FormatCursor("abc", 7))
	if err != nil || e != "abc" || s != 7 {
		t.Fatalf("round trip = %q %d %v", e, s, err)
	}
	if e, s, err := logkit.ParseCursor(""); err != nil || e != "" || s != 0 {
		t.Fatalf("empty = %q %d %v", e, s, err)
	}
	for _, bad := range []string{"nocolon", ":5", "abc:x", "abc:-1"} {
		if _, _, err := logkit.ParseCursor(bad); !errors.Is(err, logkit.ErrBadCursor) {
			t.Fatalf("ParseCursor(%q) err = %v", bad, err)
		}
	}
}

func ev(data string) streamhub.Event { return streamhub.Event{Name: "k", Data: []byte(data)} }

func TestDigestIsCanonicalForJSON(t *testing.T) {
	a := logkit.Digest(ev(`{"a":1,"b":{"x":[1,2],"y":"z"},"n":1.50}`))
	b := logkit.Digest(ev("{ \"n\": 1.50,\n \"b\": {\"y\":\"z\", \"x\": [1, 2]}, \"a\": 1 }"))
	if !bytes.Equal(a, b) {
		t.Fatal("reordered keys and whitespace must digest equal")
	}
	for name, d := range map[string]string{
		"value":  `{"a":2,"b":{"x":[1,2],"y":"z"},"n":1.50}`,
		"array":  `{"a":1,"b":{"x":[2,1],"y":"z"},"n":1.50}`,
		"number": `{"a":1,"b":{"x":[1,2],"y":"z"},"n":1.5}`, // literal text is kept: not silently equal
	} {
		if bytes.Equal(a, logkit.Digest(ev(d))) {
			t.Errorf("%s change digests equal", name)
		}
	}
	if bytes.Equal(logkit.Digest(ev("a  b")), logkit.Digest(ev("a b"))) {
		t.Error("non-JSON data must be byte-exact")
	}
	if bytes.Equal(logkit.Digest(streamhub.Event{Name: "k", Data: []byte(`{}`)}), logkit.Digest(streamhub.Event{Name: "j", Data: []byte(`{}`)})) {
		t.Error("name must be part of the digest")
	}
	// Trailing data is not one JSON value: byte-exact.
	if bytes.Equal(logkit.Digest(ev(`{"a":1} {"b":2}`)), logkit.Digest(ev(`{"a":1}{"b":2}`))) {
		t.Error("multiple values must be byte-exact")
	}
	// Large integers keep their text (no float rounding).
	if bytes.Equal(logkit.Digest(ev(`{"n":9007199254740993}`)), logkit.Digest(ev(`{"n":9007199254740992}`))) {
		t.Error("large integers lost precision")
	}
}

func TestEnvelopeExtractor(t *testing.T) {
	mesh := streamhub.Event{Name: "task.state_changed", Data: []byte(
		`{"session_id":"s1","generation":2,"visibility":"environment",` +
			`"scope":{"project_id":"p1","workstream_id":"w1","task_id":"t1","run_id":"r1","agent_urn":"msg://agent/x/y"},` +
			`"class":{"category":"state","severity":"info","sensitivity":"private","pii":true}}`)}
	want := logkit.Columns{Category: "state", Severity: "info", Sensitivity: "private", PII: true, Visibility: "environment",
		Generation: 2, ProjectID: "p1", WorkstreamID: "w1", TaskID: "t1", SessionID: "s1", AgentURN: "msg://agent/x/y", RunID: "r1"}
	if got := logkit.EnvelopeExtractor(mesh); got != want {
		t.Fatalf("mesh columns = %+v", got)
	}
	out := streamhub.Event{Data: []byte(`{"payload":{"from":"todo","to":"doing"}}`)}
	if got := logkit.EnvelopeExtractor(out).Outcome; got != "doing" {
		t.Fatalf("outcome from payload.to = %q", got)
	}
	out2 := streamhub.Event{Data: []byte(`{"payload":{"outcome":"ok","to":"x"}}`)}
	if got := logkit.EnvelopeExtractor(out2).Outcome; got != "ok" {
		t.Fatalf("outcome should win over to, got %q", got)
	}
	for _, d := range []string{`{"payload":"text"}`, `{"payload":[1,2]}`, `{"payload":null}`} {
		if got := logkit.EnvelopeExtractor(streamhub.Event{Data: []byte(d)}); got.Outcome != "" {
			t.Fatalf("%s produced outcome %q", d, got.Outcome)
		}
	}
	both := streamhub.Event{Data: []byte(`{"visibility":"mesh","class":{"visibility":"operator"}}`)}
	if v := logkit.EnvelopeExtractor(both).Visibility; v != "operator" {
		t.Fatalf("class.visibility should win, got %q", v)
	}
	for _, d := range []string{"", "not json", "[1,2]", `{"scope":"x"}`} {
		if got := logkit.EnvelopeExtractor(streamhub.Event{Data: []byte(d)}); got != (logkit.Columns{}) {
			t.Errorf("%q produced %+v, want zero Columns", d, got)
		}
	}
}

func TestMarkerRoundTripAndBehind(t *testing.T) {
	path := t.TempDir() + "/sub/log.hwm"
	if m, err := logkit.ReadMarker(path); m != nil || err != nil {
		t.Fatalf("missing marker = %v, %v", m, err)
	}
	if err := logkit.WriteMarker(path, logkit.Marker{Epoch: "e1", Streams: map[string]uint64{"s": 5}}); err != nil {
		t.Fatal(err)
	}
	m, err := logkit.ReadMarker(path)
	if err != nil || m.Epoch != "e1" || m.Streams["s"] != 5 {
		t.Fatalf("read = %+v, %v", m, err)
	}
	if _, behind := m.Behind(map[string]uint64{"s": 5}); behind {
		t.Error("equal head reported behind")
	}
	if s, behind := m.Behind(map[string]uint64{"s": 4}); !behind || s != "s" {
		t.Errorf("Behind = %q, %v", s, behind)
	}
	if _, behind := m.Behind(map[string]uint64{}); !behind {
		t.Error("missing stream (head 0) must be behind a marker of 5")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := logkit.ReadMarker(path); !errors.Is(err, logkit.ErrMarkerUnreadable) {
		t.Errorf("unreadable marker err = %v", err)
	}
	var nilm *logkit.Marker
	if _, behind := nilm.Behind(nil); behind {
		t.Error("nil marker is never behind")
	}
}
