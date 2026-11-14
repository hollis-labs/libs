package runtimeevents

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEventJSONRoundTrip(t *testing.T) {
	off := int64(2048)
	ev := Event{
		SchemaVersion: SchemaVersion,
		ID:            "evt_abc",
		Kind:          KindAgentToolUse,
		Time:          time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC),
		App:           "nanite",
		SessionID:     "ses_xyz",
		TurnID:        "turn_1",
		Sequence:      42,
		ParentID:      "evt_parent",
		RawOffset:     &off,
		Process: Process{
			PID:               12345,
			Provider:          "claude",
			Runtime:           "streaming-stdio",
			ProviderSessionID: "claude-session-uuid",
		},
		Source: Source{
			Channel:    ChannelClaudeStreamJSON,
			Confidence: ConfidenceExact,
		},
		Payload: json.RawMessage(`{"tool":"Read","args":{"path":"/tmp/x"}}`),
	}

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Event
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(ev, got) {
		t.Fatalf("round-trip mismatch:\nwant %#v\ngot  %#v", ev, got)
	}
}

func TestEventOmitsUnsetOptionals(t *testing.T) {
	ev := Event{
		SchemaVersion: SchemaVersion,
		ID:            "evt_min",
		Kind:          KindSessionReady,
		Time:          time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC),
		SessionID:     "ses_min",
		Sequence:      1,
		Source:        Source{Channel: ChannelPTY},
	}

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)

	mustOmit := []string{"app", "turn_id", "parent_id", "raw_offset", "payload", "confidence", "summary"}
	for _, key := range mustOmit {
		if strings.Contains(s, `"`+key+`"`) {
			t.Errorf("unset optional %q appears in JSON: %s", key, s)
		}
	}

	mustInclude := []string{
		`"schema_version":"1"`,
		`"id":"evt_min"`,
		`"kind":"session.ready"`,
		`"session_id":"ses_min"`,
		`"sequence":1`,
		`"channel":"pty"`,
	}
	for _, sub := range mustInclude {
		if !strings.Contains(s, sub) {
			t.Errorf("required substring missing: %s\nin: %s", sub, s)
		}
	}
}

func TestEventSummaryValidation(t *testing.T) {
	base := Event{
		SchemaVersion: SchemaVersion,
		ID:            "evt_sum",
		Kind:          "seat.waiting",
		Time:          time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC),
		Subject:       "msg://agent/tether/photon-builder",
		Sequence:      1,
		Source:        Source{Channel: ChannelHook},
	}
	for _, c := range []struct {
		name    string
		summary string
		valid   bool
	}{
		{"empty", "", true},
		{"one line", "photon-builder is waiting for approval", true},
		{"200 characters", strings.Repeat("a", MaxSummaryLen), true},
		{"200 multibyte characters", strings.Repeat("é", MaxSummaryLen), true},
		{"201 characters", strings.Repeat("a", MaxSummaryLen+1), false},
		{"line feed", "waiting\nfor approval", false},
		{"carriage return", "waiting\rfor approval", false},
	} {
		ev := base
		ev.Summary = c.summary
		if err := ev.Validate(); (err == nil) != c.valid {
			t.Errorf("%s: valid=%v, want %v (%v)", c.name, err == nil, c.valid, err)
		}
	}
}

func TestUnknownEventKindRoundTrips(t *testing.T) {
	ev := Event{
		SchemaVersion: SchemaVersion,
		ID:            "evt_x",
		Kind:          EventKind("future.kind.we.dont.know.yet"),
		Time:          time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC),
		SessionID:     "ses_x",
		Sequence:      1,
		Source:        Source{Channel: SourceChannel("future-channel")},
	}

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Event
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != ev.Kind {
		t.Errorf("unknown Kind not preserved: got %q want %q", got.Kind, ev.Kind)
	}
	if got.Source.Channel != ev.Source.Channel {
		t.Errorf("unknown Source.Channel not preserved: got %q want %q", got.Source.Channel, ev.Source.Channel)
	}
}
