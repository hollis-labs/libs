package fakeserver_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/conformance"
	"github.com/hollis-labs/go-chatstream/conformance/fakeserver"
	"github.com/hollis-labs/go-chatstream/framing"
	ssekit "github.com/hollis-labs/go-ssekit"
)

func collect(seq func(func(chatstream.Event, error) bool)) ([]chatstream.Event, error) {
	var out []chatstream.Event
	var err error
	seq(func(ev chatstream.Event, e error) bool {
		if e != nil {
			err = e
			return false
		}
		out = append(out, ev)
		return true
	})
	return out, err
}

func terminals(evs []chatstream.Event) []chatstream.Event {
	var out []chatstream.Event
	for _, e := range evs {
		if e.IsTerminal() {
			out = append(out, e)
		}
	}
	return out
}

func decodeOpts() chatstream.DecodeOptions { return conformance.DecodeOptions() }

// upstreams runs a case against both ways of serving a plan.
func upstreams(t *testing.T, p *fakeserver.Plan, run func(t *testing.T, u *fakeserver.Upstream)) {
	t.Helper()
	t.Run("socket", func(t *testing.T) { run(t, p.Serve(t)) })
	t.Run("memory", func(t *testing.T) { run(t, p.Memory()) })
}

func TestPlanServesFramesWithPositionalIDs(t *testing.T) {
	frames := echoFrames()
	frames[2].ID = "custom-3"
	upstreams(t, fakeserver.Complete(frames), func(t *testing.T, u *fakeserver.Upstream) {
		resp, err := u.Client.Get(u.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Fatalf("content type %q", ct)
		}
		var got []chatstream.Frame
		for f, err := range framing.SSE(resp.Body) {
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, f)
		}
		if len(got) != 4 {
			t.Fatalf("frames = %+v", got)
		}
		wantIDs := []string{"1", "2", "custom-3", "4"}
		for i, f := range got {
			if f.ID != wantIDs[i] || f.Event != "m" || string(f.Data) != string(frames[i].Data) {
				t.Errorf("frame %d = %+v, want id %s and the same data", i, f, wantIDs[i])
			}
		}
		if ids := u.LastEventIDs(); !slices.Equal(ids, []string{""}) {
			t.Errorf("LastEventIDs = %q", ids)
		}
	})
}

func TestCompleteStreamDecodesToASuccess(t *testing.T) {
	upstreams(t, fakeserver.Complete(echoFrames()), func(t *testing.T, u *fakeserver.Upstream) {
		evs, err := collect(fakeserver.DecodeOverHTTP(context.Background(), u.URL, echo{}, decodeOpts(), fakeserver.WithClient(u.Client)))
		if err != nil {
			t.Fatal(err)
		}
		conformance.Check(t, evs)
		last := evs[len(evs)-1]
		if last.Verb != chatstream.VerbRunFinish || last.Finish() != chatstream.FinishStop {
			t.Fatalf("last = %+v", last)
		}
	})
}

// DROP: the connection is cut after two frames. Whatever the client was in the
// middle of, the stream ends with exactly one terminal event and it is a
// truncation error, never a success.
func TestDropMidStreamIsATruncationError(t *testing.T) {
	upstreams(t, fakeserver.DropAfter(echoFrames(), 2), func(t *testing.T, u *fakeserver.Upstream) {
		evs, err := collect(fakeserver.DecodeOverHTTP(context.Background(), u.URL, echo{}, decodeOpts(), fakeserver.WithClient(u.Client)))
		if err != nil {
			t.Fatal(err)
		}
		conformance.Check(t, evs)
		terms := terminals(evs)
		if len(terms) != 1 || terms[0].Verb != chatstream.VerbRunError || terms[0].Code != chatstream.CodeUpstreamTruncated || !terms[0].Retryable {
			t.Fatalf("terminals = %+v", terms)
		}
		if !strings.Contains(terms[0].Message, "unexpected EOF") {
			t.Errorf("the cause should be in the message: %q", terms[0].Message)
		}
		var text string
		for _, e := range evs {
			text += e.Text
		}
		if text != "Hello, world" {
			t.Errorf("the two frames before the cut must be decoded, got %q", text)
		}
		for _, e := range evs {
			if e.Verb == chatstream.VerbPartStart && e.PartKind() == chatstream.PartToolCall {
				t.Errorf("the connection was cut after two frames, but the third (the tool call) was decoded")
			}
		}
	})
}

// A clean close without the terminal frame is truncation too.
func TestCleanCloseWithoutTerminalFrameIsATruncationError(t *testing.T) {
	p := fakeserver.NewPlan(echoFrames()).Send(0, 2).Close()
	upstreams(t, p, func(t *testing.T, u *fakeserver.Upstream) {
		evs, err := collect(fakeserver.DecodeOverHTTP(context.Background(), u.URL, echo{}, decodeOpts(), fakeserver.WithClient(u.Client)))
		if err != nil {
			t.Fatal(err)
		}
		if terms := terminals(evs); len(terms) != 1 || terms[0].Code != chatstream.CodeUpstreamTruncated {
			t.Fatalf("terminals = %+v", terms)
		}
		conformance.Check(t, evs)
	})
}

// STUCK: the server goes silent with the connection open. The caller gives up
// (cancels) after seeing the frames that did arrive; the run ends with a
// run.error carrying the cause. Canceling on an observed event, not on a timer,
// keeps the test free of sleeps.
func TestStuckUpstreamEndsWithTheCancellationCause(t *testing.T) {
	upstreams(t, fakeserver.Stuck(echoFrames(), 2), func(t *testing.T, u *fakeserver.Upstream) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var evs []chatstream.Event
		for ev, err := range fakeserver.DecodeOverHTTP(ctx, u.URL, echo{}, decodeOpts(), fakeserver.WithClient(u.Client)) {
			if err != nil {
				t.Fatal(err)
			}
			evs = append(evs, ev)
			if ev.Verb == chatstream.VerbPartDelta && ev.Text == ", world" {
				cancel() // both frames arrived; nothing more is coming
			}
		}
		conformance.Check(t, evs)
		terms := terminals(evs)
		if len(terms) != 1 || terms[0].Code != chatstream.CodeUpstreamTruncated || !strings.Contains(terms[0].Message, "context canceled") {
			t.Fatalf("terminals = %+v", terms)
		}
	})
}

// The same with a deadline, on fake time: the in-memory upstream goes silent
// for an hour and the timeout fires without waiting.
func TestStuckUpstreamHitsTheDeadlineOnFakeTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u := fakeserver.Stuck(echoFrames(), 2).Memory()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		start := time.Now()
		evs, err := collect(fakeserver.DecodeOverHTTP(ctx, u.URL, echo{}, decodeOpts(), fakeserver.WithClient(u.Client)))
		if err != nil {
			t.Fatal(err)
		}
		if got := time.Since(start); got != 30*time.Second {
			t.Errorf("fake elapsed = %v, want the deadline", got)
		}
		terms := terminals(evs)
		if len(terms) != 1 || !strings.Contains(terms[0].Message, "deadline exceeded") {
			t.Fatalf("terminals = %+v", terms)
		}
	})
}

func TestNon200AndConnectFailureYieldOneErrorAndNoEvents(t *testing.T) {
	upstreams(t, fakeserver.NewPlan(echoFrames()).NotFound(), func(t *testing.T, u *fakeserver.Upstream) {
		evs, err := collect(fakeserver.DecodeOverHTTP(context.Background(), u.URL, echo{}, decodeOpts(), fakeserver.WithClient(u.Client)))
		var se *fakeserver.StatusError
		if !errors.As(err, &se) || se.Code != http.StatusNotFound || len(evs) != 0 {
			t.Fatalf("events %d, err %v", len(evs), err)
		}
	})
	if _, err := collect(fakeserver.DecodeOverHTTP(context.Background(), "http://127.0.0.1:1/x", echo{}, decodeOpts())); err == nil {
		t.Error("an unreachable upstream must yield an error")
	}
}

// resumeFrames reads frames from u with go-ssekit's client, which reconnects
// with Last-Event-ID; this is how a real consumer composes resume with a decoder.
func resumeFrames(ctx context.Context, u *fakeserver.Upstream, opts ...ssekit.StreamOption) func(func(chatstream.Frame, error) bool) {
	client := ssekit.NewClient(u.Client, ssekit.WithDefaults(
		ssekit.WithBackoff([]time.Duration{time.Millisecond}, 0),
		ssekit.WithIsTerminal(func(ev ssekit.Event) bool { return strings.Contains(string(ev.Data), `"done"`) }),
	))
	newReq := func(string) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, u.URL, nil)
	}
	return func(yield func(chatstream.Frame, error) bool) {
		for ev, err := range client.Stream(ctx, newReq, opts...) {
			if err != nil {
				yield(chatstream.Frame{}, err)
				return
			}
			if !yield(chatstream.Frame{Event: ev.Name, Data: ev.Data, ID: ev.ID}, nil) {
				return
			}
		}
	}
}

func frameIDs(t *testing.T, seq func(func(chatstream.Frame, error) bool)) ([]string, error) {
	t.Helper()
	var ids []string
	var err error
	seq(func(f chatstream.Frame, e error) bool {
		if e != nil {
			err = e
			return false
		}
		ids = append(ids, f.ID)
		return true
	})
	return ids, err
}

// A drop followed by a proper resume: the client sends the last id it saw, the
// server continues from there, and the decoder sees one unbroken stream.
func TestDropThenResumeDecodesTheWholeRun(t *testing.T) {
	frames := echoFrames()
	u := fakeserver.NewPlan(frames).Send(0, 2).Drop().Send(2, 4).Close().Memory()
	ctx := context.Background()
	evs, err := collect(chatstream.DecodeFrames(ctx, echo{}.NewDecoder(decodeOpts()), resumeFrames(ctx, u)))
	if err != nil {
		t.Fatal(err)
	}
	conformance.Check(t, evs)
	if last := evs[len(evs)-1]; last.Verb != chatstream.VerbRunFinish {
		t.Fatalf("last = %+v", last)
	}
	if got := u.LastEventIDs(); !slices.Equal(got, []string{"", "2"}) {
		t.Fatalf("LastEventIDs = %q, want the resume to carry the last id seen", got)
	}
}

// OVERLAP: the reconnect replays from before the client's cursor. go-ssekit
// delivers the overlap (de-duplication is the application's), and reports it as
// a continuity failure when asked.
func TestOverlapReplaysWhatTheClientAlreadyHas(t *testing.T) {
	plan := fakeserver.Overlap(echoFrames(), 3, 2) // conn 1: 1-3; conn 2: 2-4
	ids, err := frameIDs(t, resumeFrames(context.Background(), plan.Memory()))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{"1", "2", "3", "2", "3", "4"}) {
		t.Fatalf("delivered ids = %v", ids)
	}

	u := plan.Memory()
	contiguous := ssekit.WithContinuity(func(prev, next string) bool { return next == plusOne(prev) })
	_, err = frameIDs(t, resumeFrames(context.Background(), u, contiguous))
	var gap *ssekit.GapError
	if !errors.As(err, &gap) || gap.Prev != "3" || gap.Next != "2" {
		t.Fatalf("err = %v, want a GapError from 3 to 2", err)
	}
	if got := u.LastEventIDs(); !slices.Equal(got, []string{"", "3"}) {
		t.Errorf("LastEventIDs = %q", got)
	}
}

// GAP: the reconnect skips events. Without a continuity check they are simply
// missing; with one, the stream stops with a GapError.
func TestGapSkipsEventsTheClientNeverSees(t *testing.T) {
	plan := fakeserver.Gap(echoFrames(), 2, 1) // conn 1: 1-2; conn 2: 4
	ids, err := frameIDs(t, resumeFrames(context.Background(), plan.Memory()))
	if err != nil || !slices.Equal(ids, []string{"1", "2", "4"}) {
		t.Fatalf("ids %v, err %v", ids, err)
	}

	u := plan.Memory()
	contiguous := ssekit.WithContinuity(func(prev, next string) bool { return next == plusOne(prev) })
	_, err = frameIDs(t, resumeFrames(context.Background(), u, contiguous))
	var gap *ssekit.GapError
	if !errors.As(err, &gap) || gap.Prev != "2" || gap.Next != "4" {
		t.Fatalf("err = %v, want a GapError from 2 to 4", err)
	}
	if got := u.LastEventIDs(); !slices.Equal(got, []string{"", "2"}) {
		t.Errorf("LastEventIDs = %q", got)
	}
}

func plusOne(id string) string {
	n := 0
	for _, c := range id {
		n = n*10 + int(c-'0')
	}
	return string(rune('0' + n + 1)) // ids in these tests are single digits
}

// 404 ON RESUME: the run is gone. The client stops (a 4xx is final) after one
// resume attempt, and the decoder turns that into a truncation error carrying
// the status.
func TestResumeNotFoundIsFinal(t *testing.T) {
	u := fakeserver.ResumeNotFound(echoFrames(), 2).Memory()
	ctx := context.Background()
	evs, err := collect(chatstream.DecodeFrames(ctx, echo{}.NewDecoder(decodeOpts()), resumeFrames(ctx, u)))
	if err != nil {
		t.Fatal(err)
	}
	conformance.Check(t, evs)
	terms := terminals(evs)
	if len(terms) != 1 || terms[0].Verb != chatstream.VerbRunError || terms[0].Code != chatstream.CodeUpstreamTruncated || !strings.Contains(terms[0].Message, "Not Found") {
		t.Fatalf("terminals = %+v", terms)
	}
	if got := u.LastEventIDs(); !slices.Equal(got, []string{"", "2"}) {
		t.Fatalf("LastEventIDs = %q: one resume attempt, then stop", got)
	}
}

func TestScenarioBuildersRejectBadRanges(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Send outside the frames must panic")
		}
	}()
	fakeserver.NewPlan(echoFrames()).Send(0, 9)
}

func TestMemoryUpstreamMatchesASocketForAbruptEnd(t *testing.T) {
	for name, u := range map[string]*fakeserver.Upstream{
		"socket": fakeserver.DropAfter(echoFrames(), 1).Serve(t),
		"memory": fakeserver.DropAfter(echoFrames(), 1).Memory(),
	} {
		resp, err := u.Client.Get(u.URL)
		if err != nil {
			t.Fatal(name, err)
		}
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err == nil {
			t.Errorf("%s: an abruptly ended body must surface an error", name)
		}
	}
}
