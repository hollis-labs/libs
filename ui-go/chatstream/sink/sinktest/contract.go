package sinktest

import (
	"errors"
	"strings"
	"testing"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/sink"
)

// Contract runs the tests every Encoder must pass, against fresh encoders from
// newEnc: every scenario encodes to valid SSE and flushes; nothing is accepted
// after the terminal event or after Close; Close is idempotent and signals a run
// that never ended; unknown verbs are skipped; a Writer that fails on any write
// or flush is returned as the caller's error and stays failed; a second Writer is
// refused.
func Contract(t *testing.T, newEnc func() sink.Encoder) {
	t.Helper()
	name := newEnc().Name()

	t.Run("identity", func(t *testing.T) {
		enc := newEnc()
		if enc.Name() == "" {
			t.Error("Name is empty")
		}
		if !strings.HasPrefix(enc.ContentType(), "text/event-stream") {
			t.Errorf("ContentType = %q", enc.ContentType())
		}
		h := enc.Headers()
		if h.Get("Content-Type") != enc.ContentType() {
			t.Errorf("Headers Content-Type = %q, ContentType = %q", h.Get("Content-Type"), enc.ContentType())
		}
		if h.Get("Cache-Control") == "" {
			t.Error("no Cache-Control header")
		}
	})

	t.Run("every scenario is valid SSE", func(t *testing.T) {
		for _, sc := range Scenarios() {
			frames := Play(t, newEnc(), sc)
			if len(frames) == 0 && len(sc.Events) > 0 {
				t.Errorf("%s: no frames for %d events", sc.Name, len(sc.Events))
			}
		}
	})

	t.Run("no Encode after the terminal event", func(t *testing.T) {
		for _, sc := range Scenarios() {
			if sc.Truncated {
				continue
			}
			enc := newEnc()
			rec := &Recorder{}
			for _, ev := range sc.Events {
				if err := enc.Encode(rec, ev); err != nil {
					t.Fatalf("%s: %v", sc.Name, err)
				}
			}
			before := rec.Len()
			late := chatstream.Event{V: chatstream.SchemaVersion, RunID: "run-1", Time: T0, Verb: chatstream.VerbRaw,
				Raw: &chatstream.Raw{Dialect: "x", Type: "late"}}
			err := enc.Encode(rec, late)
			if !errors.Is(err, chatstream.ErrAfterTerminal) {
				t.Errorf("%s: Encode after the terminal event returned %v, want ErrAfterTerminal", sc.Name, err)
			}
			if rec.Len() != before {
				t.Errorf("%s: %d bytes written after the terminal event", sc.Name, rec.Len()-before)
			}
		}
	})

	t.Run("Close is idempotent and Encode after Close fails", func(t *testing.T) {
		sc := Named(t, "plain_text")
		enc := newEnc()
		rec := &Recorder{}
		for _, ev := range sc.Events {
			if err := enc.Encode(rec, ev); err != nil {
				t.Fatal(err)
			}
		}
		if err := enc.Close(rec, nil); err != nil {
			t.Fatal(err)
		}
		n := rec.Len()
		if err := enc.Close(rec, nil); err != nil || rec.Len() != n {
			t.Errorf("second Close: err %v, %d extra bytes", err, rec.Len()-n)
		}
		if err := enc.Encode(rec, sc.Events[0]); !errors.Is(err, sink.ErrClosed) {
			t.Errorf("Encode after Close: %v, want ErrClosed", err)
		}
	})

	t.Run("Close signals a run that never ended", func(t *testing.T) {
		for _, sc := range Scenarios() {
			if !sc.Truncated {
				continue
			}
			frames := Play(t, newEnc(), sc)
			all := Describe(frames)
			if len(frames) == 0 {
				t.Errorf("%s: nothing written: the client is left without a signal", sc.Name)
			}
			if !strings.Contains(all, ErrCut.Error()) {
				t.Errorf("%s: the cause %q is not in the output: %s", sc.Name, ErrCut, all)
			}
		}
	})

	t.Run("unknown verbs are skipped", func(t *testing.T) {
		enc := newEnc()
		rec := &Recorder{}
		start := Named(t, "plain_text").Events[0]
		if err := enc.Encode(rec, start); err != nil {
			t.Fatal(err)
		}
		if err := enc.Encode(rec, chatstream.Event{V: chatstream.SchemaVersion, RunID: "run-1", Time: T0, Verb: "future.verb"}); err != nil {
			t.Errorf("an unknown verb must not fail the encoder: %v", err)
		}
	})

	t.Run("out-of-order and duplicate events never panic", func(t *testing.T) {
		for _, hc := range hostileSequences() {
			t.Run(hc.name, func(t *testing.T) {
				enc := newEnc()
				rec := &Recorder{}
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s panicked: %v", name, r)
					}
				}()
				for _, ev := range hc.events {
					_ = enc.Encode(rec, ev) // an error is fine; a panic is not
				}
				_ = enc.Close(rec, ErrCut)
				Parse(t, rec.Bytes()) // and whatever was written is still valid SSE
			})
		}
	})

	t.Run("a second Writer is refused", func(t *testing.T) {
		enc := newEnc()
		evs := Named(t, "plain_text").Events // run.start, message.start, part.start, delta, ...
		first := &Recorder{}
		for _, ev := range evs[:3] {
			if err := enc.Encode(first, ev); err != nil {
				t.Fatal(err)
			}
		}
		if err := enc.Encode(&Recorder{}, evs[3]); !errors.Is(err, sink.ErrWriterChanged) {
			t.Errorf("err = %v, want ErrWriterChanged", err)
		}
	})

	t.Run("a failed writer stays failed", func(t *testing.T) {
		boom := errors.New("broken pipe")
		sc := Named(t, "plain_text")
		f := &Failing{Err: boom, FailWrite: 1}
		enc := newEnc()
		var deltaErrs []error
		for _, ev := range sc.Events {
			err := enc.Encode(f, ev)
			if ev.Verb == chatstream.VerbPartDelta {
				deltaErrs = append(deltaErrs, err)
			}
		}
		for i, err := range deltaErrs {
			if !errors.Is(err, boom) {
				t.Errorf("delta %d after the writer died: %v, want the writer's error", i, err)
			}
		}
	})

	for _, kind := range []string{"write", "flush"} {
		t.Run("a failing "+kind+" is returned", func(t *testing.T) {
			boom := errors.New("broken pipe")
			for _, scName := range []string{"plain_text", "tool_call_and_result", "truncated_mid_text"} {
				sc := Named(t, scName)
				// count the writes and flushes of a good run
				good := &Failing{Err: boom}
				drive(t, newEnc(), good, sc)
				total := good.writes
				if kind == "flush" {
					total = good.flushes
				}
				if total == 0 {
					t.Fatalf("%s/%s: nothing to fail", name, scName)
				}
				for k := 1; k <= total; k++ {
					f := &Failing{Err: boom}
					if kind == "write" {
						f.FailWrite = k
					} else {
						f.FailFlush = k
					}
					errs := driveErrs(newEnc(), f, sc)
					first := -1
					for i, err := range errs {
						if err != nil {
							first = i
							break
						}
					}
					if first < 0 {
						t.Errorf("%s: %s %d of %d failed but no call returned an error", scName, kind, k, total)
						continue
					}
					if !errors.Is(errs[first], boom) {
						t.Errorf("%s: %s %d: first error %v does not wrap the writer's", scName, kind, k, errs[first])
					}
				}
			}
		})
	}
}

func drive(t *testing.T, enc sink.Encoder, w sink.Writer, sc Scenario) {
	t.Helper()
	for i, err := range driveErrs(enc, w, sc) {
		if err != nil {
			t.Fatalf("%s: call %d: %v", sc.Name, i, err)
		}
	}
}

// driveErrs feeds sc's events and then Close, returning every call's error.
func driveErrs(enc sink.Encoder, w sink.Writer, sc Scenario) []error {
	var errs []error
	for _, ev := range sc.Events {
		errs = append(errs, enc.Encode(w, ev))
	}
	var cause error
	if sc.Truncated {
		cause = sc.Cause
	}
	return append(errs, enc.Close(w, cause))
}

type hostile struct {
	name   string
	events []chatstream.Event
}

// hostileSequences are event sequences a well-formed run never produces, and
// that a resumed or corrupted stream can: no encoder may panic on them.
func hostileSequences() []hostile {
	seq := func(f func(b *Builder)) []chatstream.Event { return Build(f) }
	text := chatstream.PartText
	call := chatstream.PartToolCall
	return []hostile{
		{"duplicate open part then finish", seq(func(b *Builder) {
			b.Start()
			b.Part("a", text, nil)
			b.Part("a", text, nil)
			b.End("a", "")
			b.Finish(chatstream.FinishStop, "", nil)
		})},
		{"duplicate open part then approval", seq(func(b *Builder) {
			b.Start()
			b.Part("a", text, nil)
			b.Part("a", text, nil)
			b.End("a", "")
			b.Approval("ap", "c", chatstream.ApprovalInBand, `{"tool":"bash"}`)
		})},
		{"duplicate open part then replace_content", seq(func(b *Builder) {
			b.Start()
			b.Part("a", text, nil)
			b.Part("a", text, nil)
			b.End("a", "")
			b.Activity(sink.ActivityReplaceContent, `{"content":"x"}`)
			b.Finish(chatstream.FinishStop, "", nil)
		})},
		{"duplicate open tool call", seq(func(b *Builder) {
			b.Start()
			b.Part("c", call, Meta(sink.MetaName, "bash"))
			b.Part("c", call, Meta(sink.MetaName, "bash"))
			b.End("c", `{}`)
			b.End("c", `{}`)
			b.Finish(chatstream.FinishToolCalls, "", nil)
		})},
		{"end twice", seq(func(b *Builder) {
			b.Start()
			b.Part("a", text, nil)
			b.End("a", "")
			b.End("a", "")
			b.Finish(chatstream.FinishStop, "", nil)
		})},
		{"delta and end for parts never opened", seq(func(b *Builder) {
			b.Start()
			b.Text("ghost", "x")
			b.Frag("ghost", "{")
			b.End("ghost", "")
			b.Finish(chatstream.FinishStop, "", nil)
		})},
		{"mid-run tail without run.start", seq(func(b *Builder) {
			b.Text("a", "tail")
			b.End("a", "")
			b.Part("res", chatstream.PartToolResult, Meta(sink.MetaCallID, "c"))
			b.Text("res", "out")
			b.End("res", "")
			b.Add(chatstream.VerbStepFinish, func(e *chatstream.Event) { e.StepID = "s1" })
			b.Finish(chatstream.FinishStop, "", nil)
		})},
		{"finish and approval alone", seq(func(b *Builder) {
			b.Approval("ap", "", chatstream.ApprovalSuspend, `{}`)
			b.Finish(chatstream.FinishStop, "", nil)
		})},
	}
}

// OutOfOrderErrors checks that a stateful encoder answers events that follow
// from nothing it has seen (the tail of a run, as a resumed subscription would
// deliver it; a part opened twice) with an error wrapping sink.ErrOutOfOrder,
// instead of dropping them silently. It is not part of Contract: the native
// encoder keeps no state and passes every event through.
func OutOfOrderErrors(t *testing.T, newEnc func() sink.Encoder) {
	t.Helper()
	cases := map[string][]chatstream.Event{
		"delta for a part never opened": Build(func(b *Builder) { b.Text("t1", "tail") }),
		"end for a part never opened":   Build(func(b *Builder) { b.End("t1", "") }),
		"delta after the part ended": Build(func(b *Builder) {
			b.Start()
			b.Part("t1", chatstream.PartText, nil)
			b.End("t1", "")
			b.Text("t1", "late")
		}),
		"part opened twice": Build(func(b *Builder) {
			b.Start()
			b.Part("t1", chatstream.PartText, nil)
			b.Part("t1", chatstream.PartText, nil)
		}),
	}
	for name, evs := range cases {
		enc := newEnc()
		rec := &Recorder{}
		var err error
		for _, ev := range evs {
			if err = enc.Encode(rec, ev); err != nil {
				break
			}
		}
		if !errors.Is(err, sink.ErrOutOfOrder) {
			t.Errorf("%s: %s: err = %v, want sink.ErrOutOfOrder", enc.Name(), name, err)
		}
	}
}
