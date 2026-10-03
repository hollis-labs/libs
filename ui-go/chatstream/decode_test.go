package chatstream_test

import (
	"context"
	"errors"
	"iter"
	"testing"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/internal/decodekit"
)

// lineDecoder is a two-frame dialect: "t:<text>" is a text delta, "done"
// finishes. It exists to exercise DecodeFrames and decodekit together.
type lineDecoder struct {
	*decodekit.Base
	textOpen bool
}

func (d *lineDecoder) Decode(f chatstream.Frame) ([]chatstream.Event, error) {
	if err := d.CheckOpen(); err != nil {
		return nil, err
	}
	var out []chatstream.Event
	out = d.EnsureStarted(out)
	switch s := string(f.Data); s {
	case "done":
		out = d.Unwind(out)
		fin := d.Event(chatstream.VerbRunFinish)
		fin.Reason = string(chatstream.FinishStop)
		out = d.Emit(out, fin)
	default:
		if !d.textOpen {
			st := d.Event(chatstream.VerbPartStart)
			st.PartID, st.Kind = "t", string(chatstream.PartText)
			out = d.Emit(out, st)
			d.textOpen = true
		}
		dl := d.Event(chatstream.VerbPartDelta)
		dl.PartID, dl.Text = "t", s
		out = d.Emit(out, dl)
	}
	return out, nil
}

func (d *lineDecoder) Close(cause error) []chatstream.Event { return d.Base.Close(cause) }

func frames(datas ...string) iter.Seq2[chatstream.Frame, error] {
	return func(yield func(chatstream.Frame, error) bool) {
		for _, s := range datas {
			if !yield(chatstream.Frame{Data: []byte(s)}, nil) {
				return
			}
		}
	}
}

func collect(t *testing.T, seq iter.Seq2[chatstream.Event, error]) ([]chatstream.Event, error) {
	t.Helper()
	var out []chatstream.Event
	for ev, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func verbs(evs []chatstream.Event) []chatstream.Verb {
	var out []chatstream.Verb
	for _, e := range evs {
		out = append(out, e.Verb)
	}
	return out
}

func newDec() *lineDecoder {
	return &lineDecoder{Base: decodekit.New(chatstream.DecodeOptions{RunID: "r"})}
}

func TestDecodeFramesCompleteStream(t *testing.T) {
	evs, err := collect(t, chatstream.DecodeFrames(context.Background(), newDec(), frames("hi", "there", "done")))
	if err != nil {
		t.Fatal(err)
	}
	want := []chatstream.Verb{chatstream.VerbRunStart, chatstream.VerbPartStart, chatstream.VerbPartDelta, chatstream.VerbPartDelta, chatstream.VerbPartEnd, chatstream.VerbRunFinish}
	if got := verbs(evs); len(got) != len(want) {
		t.Fatalf("verbs = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("verbs = %v, want %v", got, want)
			}
		}
	}
}

// The upstream ends without the terminal frame: the sequence still ends with
// exactly one terminal event, and it is an error, never a success.
func TestDecodeFramesTruncationIsAnErrorEvent(t *testing.T) {
	evs, err := collect(t, chatstream.DecodeFrames(context.Background(), newDec(), frames("hi")))
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1]
	if last.Verb != chatstream.VerbRunError || last.Code != chatstream.CodeUpstreamTruncated || !last.Retryable {
		t.Fatalf("last = %+v", last)
	}
	var terms int
	for _, e := range evs {
		if e.IsTerminal() {
			terms++
		}
	}
	if terms != 1 {
		t.Fatalf("%d terminal events in %v", terms, verbs(evs))
	}
	if evs[len(evs)-2].Verb != chatstream.VerbPartEnd {
		t.Fatalf("the open part must be closed before the terminal event: %v", verbs(evs))
	}
}

func TestDecodeFramesFrameErrorAndContextCancelBecomeTruncation(t *testing.T) {
	boom := errors.New("connection reset")
	failing := func(yield func(chatstream.Frame, error) bool) {
		if !yield(chatstream.Frame{Data: []byte("hi")}, nil) {
			return
		}
		yield(chatstream.Frame{}, boom)
	}
	evs, err := collect(t, chatstream.DecodeFrames(context.Background(), newDec(), failing))
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1]
	if last.Code != chatstream.CodeUpstreamTruncated || last.Message == "" || !errors.Is(boom, boom) {
		t.Fatalf("last = %+v", last)
	}
	if got := last.Message; got == "" || !contains(got, "connection reset") {
		t.Errorf("the cause should be in the message: %q", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	evs, err = collect(t, chatstream.DecodeFrames(ctx, newDec(), frames("hi")))
	if err != nil || evs[len(evs)-1].Verb != chatstream.VerbRunError {
		t.Fatalf("canceled context: %v %v", verbs(evs), err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDecodeFramesStopsReadingAfterTheTerminalEvent(t *testing.T) {
	pulled := 0
	src := func(yield func(chatstream.Frame, error) bool) {
		for _, s := range []string{"hi", "done", "late", "later"} {
			pulled++
			if !yield(chatstream.Frame{Data: []byte(s)}, nil) {
				return
			}
		}
	}
	evs, err := collect(t, chatstream.DecodeFrames(context.Background(), newDec(), src))
	if err != nil {
		t.Fatal(err)
	}
	if pulled != 2 {
		t.Errorf("pulled %d frames, want 2 (nothing is read after the terminal event)", pulled)
	}
	if evs[len(evs)-1].Verb != chatstream.VerbRunFinish {
		t.Errorf("last = %v", verbs(evs))
	}
}

func TestDecodeFramesConsumerCanStopEarly(t *testing.T) {
	for range chatstream.DecodeFrames(context.Background(), newDec(), frames("a", "b", "c")) {
		break
	}
}

func TestBaseNeverEmitsPastTheTerminalEventAndCloseIsIdempotent(t *testing.T) {
	d := newDec()
	if _, err := d.Decode(chatstream.Frame{Data: []byte("done")}); err != nil {
		t.Fatal(err)
	}
	if !d.Terminated() {
		t.Fatal("not terminated")
	}
	late, err := d.Decode(chatstream.Frame{Data: []byte("late text")})
	if err != nil || len(late) != 0 {
		t.Fatalf("emitted after the terminal event: %v %v", late, err)
	}
	if got := d.Close(nil); len(got) != 0 {
		t.Fatalf("Close after a terminal event added %v", verbs(got))
	}
	if _, err := d.Decode(chatstream.Frame{Data: []byte("x")}); !errors.Is(err, chatstream.ErrDecoderClosed) {
		t.Fatalf("Decode after Close: %v", err)
	}
}

func TestBaseTruncatedBracketsAnUnstartedRun(t *testing.T) {
	got := newDec().Close(nil)
	if len(got) != 2 || got[0].Verb != chatstream.VerbRunStart || got[1].Verb != chatstream.VerbRunError {
		t.Fatalf("an empty stream must still be bracketed: %v", verbs(got))
	}
}
