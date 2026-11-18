package chattest

import (
	"context"
	"testing"
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/conformance"
)

// failTimeout bounds how long a check waits before failing. It is a safety
// net, never a synchronization mechanism.
const failTimeout = 30 * time.Second

// RunnerHarness describes the runner under test.
type RunnerHarness struct {
	// New returns a fresh runner.
	New func(t *testing.T) chat.Runner
	// Text is a turn the runner completes on its own. Default "hello".
	Text string
	// LongText is a turn that stays in progress until it is canceled. Empty
	// skips the cancel case.
	LongText string
	// ApprovalText is a turn that raises an approval.request and, once it is
	// allowed, runs to its end. Empty skips the approval case.
	ApprovalText string
}

// CheckRun fails t unless events is a well-formed run as a runner may emit
// it: run.start first, exactly one terminal event and nothing after it, every
// part, message and step closed, seq left zero, no user message, and final on
// the part.end of every part that was not streamed (the Service's finalizer
// fills the rest).
func CheckRun(t testing.TB, events []chatstream.Event) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("the run emitted no events")
	}
	norm := make([]chatstream.Event, len(events))
	var fin chat.Finalizer
	kinds := map[string]chatstream.PartKind{}
	for i, ev := range events {
		if ev.Seq != 0 {
			t.Errorf("event %d (%s): seq is %d; a runner leaves it zero", i, ev.Verb, ev.Seq)
		}
		if ev.Verb == chatstream.VerbMessageStart && ev.Role == "user" {
			t.Errorf("event %d: the runner emitted the user's message; the Service does that", i)
		}
		if ev.Verb == chatstream.VerbPartStart {
			kinds[ev.PartID] = ev.PartKind()
		}
		ev = fin.Apply(ev)
		if ev.Verb == chatstream.VerbPartEnd && !chat.HasFinal(kinds[ev.PartID], ev.Final) {
			t.Errorf("event %d: part.end of %s part %q has no usable final and the part was not streamed", i, kinds[ev.PartID], ev.PartID)
		}
		// What the Service sets itself is not the runner's to get right.
		ev.RunID, ev.V = "run", chatstream.SchemaVersion
		if ev.Time.IsZero() {
			ev.Time = time.Unix(1, 0)
		}
		if ev.Verb == chatstream.VerbApprovalRequest && ev.Mode == "" {
			ev.Mode = chatstream.ApprovalInBand
		}
		norm[i] = ev
	}
	conformance.Check(t, norm)
}

// collect reads a run to its end. each, when set, sees every event as it
// arrives.
func collect(t testing.TB, run chat.Run, each func(chatstream.Event)) []chatstream.Event {
	t.Helper()
	type result struct {
		evs []chatstream.Event
		err error
	}
	done := make(chan result, 1)
	go func() {
		var res result
		for ev, err := range run.Events() {
			if err != nil {
				res.err = err
				break
			}
			res.evs = append(res.evs, ev)
			if each != nil {
				each(ev)
			}
		}
		done <- res
	}()
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("the run's events failed: %v", res.err)
		}
		return res.evs
	case <-time.After(failTimeout):
		t.Fatal("the run did not end")
		return nil
	}
}

// RunnerConformance checks a runner against the runner rules of the wire.
func RunnerConformance(t *testing.T, h RunnerHarness) {
	t.Helper()
	ctx := context.Background()
	if h.Text == "" {
		h.Text = "hello"
	}
	turn := func(text string) chat.Turn {
		return chat.Turn{ConversationID: "conv_conformance", RunID: "run_conformance", Text: text}
	}
	start := func(t *testing.T, r chat.Runner, text string) chat.Run {
		t.Helper()
		run, err := r.Start(ctx, turn(text))
		if err != nil || run == nil {
			t.Fatalf("Start = %v, %v", run, err)
		}
		return run
	}

	t.Run("RunIsWellFormed", func(t *testing.T) {
		evs := collect(t, start(t, h.New(t), h.Text), nil)
		CheckRun(t, evs)
		if evs[0].Verb != chatstream.VerbRunStart {
			t.Errorf("first event is %s, want run.start", evs[0].Verb)
		}
	})

	t.Run("ContextIsAccepted", func(t *testing.T) {
		tn := turn(h.Text)
		tn.Context = []byte(`{"desk":["a","b"]}`)
		tn.Meta = []byte(`{"auto":true}`)
		run, err := h.New(t).Start(ctx, tn)
		if err != nil {
			t.Fatalf("Start with context and meta: %v", err)
		}
		CheckRun(t, collect(t, run, nil))
	})

	t.Run("InfoAndReplaced", func(t *testing.T) {
		r := h.New(t)
		if _, err := r.Info(ctx, "conv_conformance"); err != nil {
			t.Errorf("Info: %v", err)
		}
		if r.Replaced() == nil {
			t.Error("Replaced returned nil; return a channel that is never sent on")
		}
		if err := r.Record(ctx, turn(h.Text), "an answer the host gave"); err != nil {
			t.Errorf("Record: %v", err)
		}
	})

	t.Run("CancelYieldsRunAbort", func(t *testing.T) {
		if h.LongText == "" {
			t.Skip("no LongText")
		}
		run := start(t, h.New(t), h.LongText)
		canceled := false
		evs := collect(t, run, func(chatstream.Event) {
			if !canceled {
				canceled = true
				run.Cancel()
				run.Cancel() // more than once is allowed
			}
		})
		CheckRun(t, evs)
		if last := evs[len(evs)-1]; last.Verb != chatstream.VerbRunAbort {
			t.Errorf("a canceled run ended with %s, want run.abort", last.Verb)
		}
	})

	t.Run("ApprovalBlocksUntilAnswered", func(t *testing.T) {
		if h.ApprovalText == "" {
			t.Skip("no ApprovalText")
		}
		run := start(t, h.New(t), h.ApprovalText)
		asked := 0
		evs := collect(t, run, func(ev chatstream.Event) {
			if ev.Verb != chatstream.VerbApprovalRequest {
				return
			}
			asked++
			if err := run.Answer(ctx, "no-such-approval", chat.Decision{Allow: true, Scope: chat.ScopeOnce}); err == nil {
				t.Error("Answer for an unknown approval returned nil")
			}
			// Answer from another goroutine: the runner may only take the
			// answer once this event has been consumed.
			go func() {
				if err := run.Answer(ctx, ev.ApprovalID, chat.Decision{Allow: true, Scope: chat.ScopeOnce}); err != nil {
					t.Errorf("Answer: %v", err)
				}
			}()
		})
		CheckRun(t, evs)
		if asked == 0 {
			t.Error("the approval turn raised no approval.request")
		}
		if evs[len(evs)-1].Verb != chatstream.VerbRunFinish {
			t.Errorf("an allowed run ended with %s, want run.finish", evs[len(evs)-1].Verb)
		}
	})
}
