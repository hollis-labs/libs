package decodekit_test

import (
	"encoding/json"
	"fmt"
	"testing"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/internal/decodekit"
)

// A malformed frame is kept as a raw event. Its bytes are not JSON, but the
// event must still marshal, or one bad frame would poison the whole stream.
func TestRawEventSurvivesMarshalWhateverTheBytes(t *testing.T) {
	b := decodekit.New(chatstream.DecodeOptions{RunID: "r"})
	for name, payload := range map[string]string{"valid": `{"a":1}`, "not json": `oops {`, "empty": ``, "binary": "\xff\xfe"} {
		ev := b.RawEvent("d", "malformed", []byte(payload))
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var back chatstream.Event
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if got := string(b.RawEvent("d", "t", []byte(`{"a":1}`)).Raw.Payload); got != `{"a":1}` {
		t.Errorf("valid JSON must be kept verbatim: %s", got)
	}
	var s string
	if err := json.Unmarshal(b.RawEvent("d", "t", []byte("oops {")).Raw.Payload, &s); err != nil || s != "oops {" {
		t.Errorf("invalid JSON must round trip as a string: %q %v", s, err)
	}
}

func TestUnwindClosesInnermostFirst(t *testing.T) {
	b := decodekit.New(chatstream.DecodeOptions{RunID: "r"})
	var out []chatstream.Event
	step := b.Event(chatstream.VerbStepStart)
	step.StepID = "s"
	msg := b.Event(chatstream.VerbMessageStart)
	msg.MessageID = "m"
	p1, p2 := b.Event(chatstream.VerbPartStart), b.Event(chatstream.VerbPartStart)
	p1.PartID, p2.PartID = "p1", "p2"
	for _, e := range []chatstream.Event{step, msg, p1, p2} {
		out = b.Emit(out, e)
	}
	got := b.Unwind(nil)
	want := []struct {
		v  chatstream.Verb
		id string
	}{{chatstream.VerbPartEnd, "p2"}, {chatstream.VerbPartEnd, "p1"}, {chatstream.VerbMessageEnd, ""}, {chatstream.VerbStepFinish, "s"}}
	if len(got) != len(want) {
		t.Fatalf("unwind = %v", got)
	}
	for i, w := range want {
		id := got[i].PartID + got[i].StepID
		if got[i].Verb != w.v || (w.id != "" && id != w.id) {
			t.Errorf("unwind[%d] = %s %s, want %s %s", i, got[i].Verb, id, w.v, w.id)
		}
	}
	if len(b.OpenParts()) != 0 {
		t.Error("parts still open after Unwind")
	}
}

func TestPartOpenTracksDuplicatesAndEnds(t *testing.T) {
	b := decodekit.New(chatstream.DecodeOptions{RunID: "r"})
	var out []chatstream.Event
	open := func(id string) {
		ev := b.Event(chatstream.VerbPartStart)
		ev.PartID = id
		out = b.Emit(out, ev)
	}
	end := func(id string) {
		ev := b.Event(chatstream.VerbPartEnd)
		ev.PartID = id
		out = b.Emit(out, ev)
	}
	for i := 0; i < 5000; i++ {
		open(fmt.Sprintf("p%d", i))
	}
	if !b.PartOpen("p0") || !b.PartOpen("p4999") || b.PartOpen("nope") {
		t.Fatal("PartOpen is wrong for open and unknown ids")
	}
	end("p0")
	if b.PartOpen("p0") || len(b.OpenParts()) != 4999 {
		t.Errorf("after end: PartOpen(p0) %v, open %d", b.PartOpen("p0"), len(b.OpenParts()))
	}
	out = b.Unwind(out)
	if b.PartOpen("p1") || len(b.OpenParts()) != 0 {
		t.Error("Unwind left parts open")
	}
}

func TestLimitExceededIsNonRetryableAndClosesEverything(t *testing.T) {
	b := decodekit.New(chatstream.DecodeOptions{RunID: "r"})
	out := b.EnsureStarted(nil)
	ev := b.Event(chatstream.VerbPartStart)
	ev.PartID = "p"
	out = b.Emit(out, ev)
	out = b.LimitExceeded(out, "things", 3)
	fin := out[len(out)-1]
	if fin.Verb != chatstream.VerbRunError || fin.Code != chatstream.CodeLimitExceeded || fin.Retryable || !b.Terminated() || b.PartOpen("p") {
		t.Errorf("terminal %+v, open %v", fin, b.PartOpen("p"))
	}
}
