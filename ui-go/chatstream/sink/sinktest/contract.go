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
