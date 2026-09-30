package native_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/sink"
	"github.com/hollis-labs/go-chatstream/sink/native"
	"github.com/hollis-labs/go-chatstream/sink/sinktest"
)

// What native writes decodes straight back into the events that went in, and
// the frame id is the event's Seq: that is what makes it resumable.
func TestRoundTripThroughSSE(t *testing.T) {
	for _, sc := range sinktest.Scenarios() {
		if sc.Truncated {
			continue
		}
		frames := sinktest.Parse(t, playBytes(t, newEnc(), sc))
		if len(frames) != len(sc.Events) {
			t.Fatalf("%s: %d frames for %d events", sc.Name, len(frames), len(sc.Events))
		}
		for i, f := range frames {
			var got chatstream.Event
			if err := json.Unmarshal([]byte(f.Data), &got); err != nil {
				t.Fatalf("%s frame %d: %v", sc.Name, i, err)
			}
			want := sc.Events[i]
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(want)
			if string(gj) != string(wj) {
				t.Errorf("%s event %d did not round trip:\n got %s\nwant %s", sc.Name, i, gj, wj)
			}
			if f.ID != strconv.FormatUint(want.Seq, 10) || f.Event != string(want.Verb) {
				t.Errorf("%s frame %d: id %q event %q, want %d %s", sc.Name, i, f.ID, f.Event, want.Seq, want.Verb)
			}
			if !reflect.DeepEqual(got.Verb, want.Verb) {
				t.Errorf("verb changed")
			}
		}
	}
}

func playBytes(t *testing.T, enc sink.Encoder, sc sinktest.Scenario) []byte {
	t.Helper()
	rec := &sinktest.Recorder{}
	for _, ev := range sc.Events {
		if err := enc.Encode(rec, ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := enc.Close(rec, nil); err != nil {
		t.Fatal(err)
	}
	return rec.Bytes()
}

func TestAnEventWithoutSeqIsWrittenWithoutAnID(t *testing.T) {
	rec := &sinktest.Recorder{}
	ev := sinktest.Named(t, "plain_text").Events[0]
	ev.Seq = 0
	if err := newEnc().Encode(rec, ev); err != nil {
		t.Fatal(err)
	}
	f := sinktest.Parse(t, rec.Bytes())
	if len(f) != 1 || f[0].ID != "" {
		t.Fatalf("frames = %+v", f)
	}
}

// Nothing is translated or dropped: a gap, raw and activity events pass as they are.
func TestNothingIsDropped(t *testing.T) {
	for _, name := range []string{"gap_in_stream", "raw_and_activity"} {
		sc := sinktest.Named(t, name)
		frames := sinktest.Play(t, newEnc(), sc)
		if len(frames) != len(sc.Events) {
			t.Errorf("%s: %d frames for %d events", name, len(frames), len(sc.Events))
		}
	}
}

func TestCloseOfAnUnfinishedRunSynthesizesAnIDLessRunError(t *testing.T) {
	sc := sinktest.Named(t, "truncated_mid_text")
	frames := sinktest.Play(t, newEnc(), sc)
	last := frames[len(frames)-1]
	var ev chatstream.Event
	if err := json.Unmarshal([]byte(last.Data), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Verb != chatstream.VerbRunError || ev.Code != chatstream.CodeStreamLost || !ev.Retryable || ev.RunID != "run-1" || !ev.Time.Equal(sinktest.T0) {
		t.Errorf("synthesized event = %+v", ev)
	}
	if ev.Seq != 0 {
		t.Errorf("a synthesized event is not in the hub's log and must have no seq, got %d", ev.Seq)
	}
	if last.Event != "run.error" {
		t.Errorf("event name %q", last.Event)
	}
	// it is a terminal event, so a native client's reducer accepts it
	evs := sc.Events
	var all []chatstream.Event
	all = append(all, evs...)
	all = append(all, ev)
	m, err := chatstream.Reduce(all[:len(all)-1], nil)
	if err != nil || m.Status != chatstream.StatusStreaming {
		t.Fatal(err)
	}
	if _, err := chatstream.Reduce(all, nil); err != nil {
		t.Errorf("Reduce rejects the synthesized terminal event: %v", err)
	}
}

func TestCloseAfterTheTerminalEventWritesNothing(t *testing.T) {
	sc := sinktest.Named(t, "plain_text")
	enc := newEnc()
	rec := &sinktest.Recorder{}
	for _, ev := range sc.Events {
		_ = enc.Encode(rec, ev)
	}
	n := rec.Len()
	if err := enc.Close(rec, errors.New("ignored")); err != nil || rec.Len() != n {
		t.Fatalf("err %v, %d extra bytes", err, rec.Len()-n)
	}
}

func TestHeaders(t *testing.T) {
	h := native.New().Headers()
	if h.Get("Content-Type") != "text/event-stream" || h.Get("Cache-Control") != "no-cache, no-transform" || h.Get("X-Accel-Buffering") != "no" {
		t.Errorf("headers = %v", h)
	}
	if native.New().Name() != "native" {
		t.Error("Name")
	}
}

func TestDefaultClockIsUsed(t *testing.T) {
	rec := &sinktest.Recorder{}
	enc := native.New()
	if err := enc.Close(rec, nil); err != nil {
		t.Fatal(err)
	}
	var ev chatstream.Event
	if err := json.Unmarshal([]byte(sinktest.Parse(t, rec.Bytes())[0].Data), &ev); err != nil || ev.Time.IsZero() {
		t.Errorf("time = %v, err %v", ev.Time, err)
	}
}
