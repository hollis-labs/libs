package chat_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
	"github.com/hollis-labs/libs/agent/chat/chattest"
	"github.com/hollis-labs/libs/agent/chat/memstore"
	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
)

func bg() context.Context { return context.Background() }

// hook is a recording Intercept.
type hook struct {
	mu    sync.Mutex
	calls int
	f     func(turn *chat.Turn) chat.Outcome
}

func (h *hook) intercept(_ context.Context, turn *chat.Turn) chat.Outcome {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()
	return h.f(turn)
}

func (h *hook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func TestInterceptReject(t *testing.T) {
	h := &hook{f: func(*chat.Turn) chat.Outcome { return chat.Reject("not_now", "the desk is busy") }}
	e := newEnv(t, withConfig(func(c *chat.Config) { c.Intercept = h.intercept }))
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	head := e.head(conv)

	out := e.wantCode(http.StatusUnprocessableEntity, "not_now", "POST", "/conversations/"+conv+"/turns",
		obj{"attachment": a, "text": "hi", "request_id": "r1"})
	if out["message"] != "the desk is busy" {
		t.Fatalf("422 body = %v", out)
	}
	// Nothing was published: the turn did not happen. Not even the lease moved.
	v := e.get(conv)
	if uint64(v["head"].(float64)) != head || v["active_run"] != nil || v["lease"] != nil {
		t.Fatalf("after a rejected turn: %v (head was %d)", v, head)
	}
	if len(e.runner.Turns()) != 0 || len(e.runner.Recorded()) != 0 {
		t.Fatal("a rejected turn reached the runner")
	}
	// A Go caller gets the same refusal as a typed error.
	_, err := e.svc.Submit(bg(), conv, chat.Turn{Text: "hi"})
	var rej *chat.RejectedError
	if !errors.As(err, &rej) || rej.Code != "not_now" || !errors.Is(err, chat.ErrRejected) {
		t.Fatalf("Submit err = %v", err)
	}
	// A rejected turn is not remembered: the retry is asked again.
	e.wantCode(http.StatusUnprocessableEntity, "not_now", "POST", "/conversations/"+conv+"/turns",
		obj{"attachment": a, "text": "hi", "request_id": "r1"})
	if h.count() != 3 {
		t.Fatalf("the hook was called %d times, want 3", h.count())
	}
}

func TestInterceptAnswer(t *testing.T) {
	h := &hook{f: func(turn *chat.Turn) chat.Outcome {
		if strings.HasPrefix(turn.Text, "/time") {
			return chat.Answer("It is noon.")
		}
		// The hook may replace Context and Meta for a turn that runs.
		turn.Context = []byte(`{"from":"hook"}`)
		turn.Meta = []byte(`{"routed":true}`)
		return chat.Continue()
	}}
	e := newEnv(t, withConfig(func(c *chat.Config) { c.Intercept = h.intercept }))
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "0")

	runID, _ := e.submit(conv, a, "/time please")
	evs := s.until(terminal)
	run := ofRun(evs, runID)
	wellFormed(t, run)
	want := "run.start message.start part.start part.end message.end message.start part.start part.end message.end run.finish"
	if got := verbs(run); got != want {
		t.Fatalf("host-answered run = %s", got)
	}
	if run[0].Provider != chat.ProviderHost {
		t.Fatalf("run.start provider = %q, want host", run[0].Provider)
	}
	msgs := committed(t, run)
	if msgs[0].Role != "user" || msgs[0].Parts[0] != "text:/time please" || msgs[1].Role != "assistant" || msgs[1].Parts[0] != "text:It is noon." {
		t.Fatalf("messages = %+v", msgs)
	}
	if run[len(run)-1].Reason != string(chatstream.FinishStop) {
		t.Fatalf("run.finish reason = %q", run[len(run)-1].Reason)
	}
	// The runner was not called, and was told afterwards.
	rec := e.runner.Recorded()
	if len(e.runner.Turns()) != 0 || len(rec) != 1 || rec[0].Answer != "It is noon." || rec[0].Turn.Text != "/time please" ||
		rec[0].Turn.RunID != runID || rec[0].Turn.ConversationID != conv {
		t.Fatalf("turns %d, recorded %+v", len(e.runner.Turns()), rec)
	}
	if e.get(conv)["active_run"] != nil {
		t.Fatal("a host-answered run left active_run set")
	}

	// A turn that continues carries the hook's context to the runner and the
	// hook's meta to the stream.
	next, _ := e.submit(conv, a, "hello")
	evs = s.until(terminal)
	user := ofRun(evs, next)[1]
	if !user.MetaBool("routed") {
		t.Fatalf("user message meta = %v", user.Meta)
	}
	turns := e.runner.Turns()
	if len(turns) != 1 || string(turns[0].Context) != `{"from":"hook"}` {
		t.Fatalf("runner turns = %d", len(turns))
	}
}

func TestGoSubmitNeedsNoLease(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	holder := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	e.must(http.StatusOK, "POST", "/conversations/"+conv+"/lease", obj{"attachment": holder})
	viewer := e.attach(conv, chat.RoleViewer, chat.StreamDeltas)
	s := e.stream(conv, viewer, "head")

	// Somebody else holds the lease; a reflex submits anyway.
	receipt, err := e.svc.Submit(bg(), conv, chat.Turn{Text: "hold", Meta: []byte(`{"auto":true}`), Context: []byte(`"ctx"`)})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	evs := s.until(deltaText("Working"))
	if evs[0].Seq != receipt.Cursor+1 || evs[0].RunID != receipt.RunID {
		t.Fatalf("first event seq %d run %q; receipt %+v", evs[0].Seq, evs[0].RunID, receipt)
	}
	if user := evs[1]; user.Role != "user" || !user.MetaBool("auto") || user.MetaString("request_id") != "" {
		t.Fatalf("user message = %s", mustJSON(t, user))
	}
	if l := e.get(conv)["lease"].(obj); l["attachment"] != holder {
		t.Fatalf("a Go submit moved the lease: %v", l)
	}
	// One active run per conversation, for Go callers too.
	if _, err := e.svc.Submit(bg(), conv, chat.Turn{Text: "another"}); !errors.Is(err, chat.ErrBusy) {
		t.Fatalf("second Submit err = %v, want ErrBusy", err)
	}
	e.runner.Release("hold")
	s.until(terminal)
	if _, err := e.svc.Submit(bg(), conv, chat.Turn{}); !errors.Is(err, chat.ErrInvalid) {
		t.Fatalf("empty Submit err = %v", err)
	}
	if _, err := e.svc.Submit(bg(), "conv_nope", chat.Turn{Text: "x"}); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("Submit to an unknown conversation err = %v", err)
	}
}

func TestSlowConsumerDoesNotBlockTheRun(t *testing.T) {
	const chunks = 200
	words := make([]string, chunks)
	for i := range words {
		words[i] = "w "
	}
	e := newEnv(t,
		withConfig(func(c *chat.Config) { c.StreamBuffer = 8 }),
		withRunner(chattest.WithScript(func(chat.Turn) []chattest.Step { return chattest.Reply(words...) })))
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)

	// A subscriber that is attached and never reads.
	slow, err := e.svc.Events(bg(), conv, a, chat.FromHead())
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()

	receipt, err := e.svc.SubmitAs(bg(), conv, chat.Submission{Attachment: a, Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	// The run finishes although nobody drains the slow subscriber, and every
	// event of it is in the log.
	ctx, cancel := context.WithTimeout(bg(), wait)
	defer cancel()
	for {
		v, verr := e.svc.Conversation(ctx, conv)
		if verr != nil {
			t.Fatal(verr)
		}
		if v.ActiveRun == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the run did not finish with a stalled subscriber attached")
		case <-time.After(time.Millisecond):
		}
	}
	var all []streamhub.Record
	for rec, err := range e.log.After(ctx, conv, streamhub.Seq(receipt.Cursor)) {
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, rec)
	}
	deltas := 0
	for _, rec := range all {
		if rec.Name == string(chatstream.VerbPartDelta) {
			deltas++
		}
	}
	if deltas != chunks || all[len(all)-1].Name != string(chatstream.VerbRunFinish) {
		t.Fatalf("the log holds %d deltas and ends with %s; want %d and run.finish", deltas, all[len(all)-1].Name, chunks)
	}

	// The slow one is closed with the cursor to resume from, having lost
	// nothing it was handed.
	var got []chatstream.Event
	var slowErr *streamhub.SlowConsumerError
	for {
		ev, nerr := slow.Next(ctx)
		if nerr != nil {
			if !errors.As(nerr, &slowErr) {
				t.Fatalf("slow subscriber ended with %v, want a SlowConsumerError", nerr)
			}
			break
		}
		got = append(got, ev)
	}
	if len(got) == 0 || len(got) > 9 {
		t.Fatalf("slow subscriber was handed %d events with a buffer of 8", len(got))
	}
	if uint64(slowErr.LastDelivered) != slow.Last() || slow.Last() != got[len(got)-1].Seq {
		t.Fatalf("resume cursor %d, last event %d", slowErr.LastDelivered, got[len(got)-1].Seq)
	}
	resumed, err := e.svc.Events(bg(), conv, a, chat.After(slow.Last()))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	for {
		ev, err := resumed.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
		if ev.IsTerminal() {
			break
		}
	}
	// The run is history by now, so the resumed part has no deltas; it still
	// neither repeats nor reorders, and ends at the same event.
	increasing(t, got)
	if got[len(got)-1].Seq != uint64(all[len(all)-1].Seq) {
		t.Fatalf("resumed stream ended at %d, the run at %d", got[len(got)-1].Seq, all[len(all)-1].Seq)
	}
}

// ---- Observe: runs the runner started ----

func TestObservePublishesARunTheRunnerStarted(t *testing.T) {
	h := &hook{f: func(*chat.Turn) chat.Outcome { return chat.Reject("no", "the hook must not see observed runs") }}
	e := newEnv(t, withConfig(func(c *chat.Config) { c.Intercept = h.intercept }))
	conv := e.conversation()
	viewer := e.attach(conv, chat.RoleViewer, chat.StreamDeltas)
	s := e.stream(conv, viewer, "head")

	// Nobody holds the lease, and the hook would refuse: neither applies.
	steps := chattest.Reply("typed ", "in the terminal")
	for i := range steps {
		steps[i].Event.RunID = "session-42-turn-3"
	}
	turn := chat.Turn{Text: "ls -la", Meta: []byte(`{"runner_run":"session-42-turn-3"}`), RunID: "ignored", ConversationID: "ignored"}
	if err := e.svc.Observe(bg(), conv, turn, e.runner.Play(steps)); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	evs := s.until(terminal)
	contiguous(t, evs, evs[0].Seq)
	runID := evs[0].RunID
	if !strings.HasPrefix(runID, "run_") {
		t.Fatalf("run_id = %q; the Service issues it", runID)
	}
	for _, ev := range evs {
		if ev.RunID != runID {
			t.Fatalf("%s carries run_id %q, want the Service's %q", ev.Verb, ev.RunID, runID)
		}
	}
	wellFormed(t, evs)
	if evs[0].Provider != "scripted" {
		t.Fatalf("run.start = %s", mustJSON(t, evs[0]))
	}
	user := evs[1]
	if user.Verb != chatstream.VerbMessageStart || user.Role != "user" || user.MetaString("origin") != chat.OriginRunner ||
		user.MetaString("runner_run") != "session-42-turn-3" {
		t.Fatalf("user message.start = %s", mustJSON(t, user))
	}
	msgs := committed(t, evs)
	if msgs[0].Parts[0] != "text:ls -la" || msgs[1].Parts[0] != "text:typed in the terminal" {
		t.Fatalf("messages = %+v", msgs)
	}
	if h.count() != 0 || len(e.runner.Turns()) != 0 {
		t.Fatalf("hook calls %d, runner starts %d; want neither", h.count(), len(e.runner.Turns()))
	}
	if v := e.get(conv); v["lease"] != nil || v["active_run"] != nil {
		t.Fatalf("after an observed run: %v", v)
	}

	// With no text known there is no user message.
	if err := e.svc.Observe(bg(), conv, chat.Turn{}, e.runner.Play(chattest.Reply("woke up"))); err != nil {
		t.Fatalf("Observe without text: %v", err)
	}
	evs = s.until(terminal)
	if got := verbs(evs); got != "run.start message.start part.start part.delta part.end message.end run.finish" {
		t.Fatalf("observed run without text = %s", got)
	}
	if evs[1].Role != "assistant" {
		t.Fatalf("first message role = %q", evs[1].Role)
	}

	if err := e.svc.Observe(bg(), conv, chat.Turn{}, nil); !errors.Is(err, chat.ErrInvalid) {
		t.Fatalf("Observe with a nil run err = %v", err)
	}
	if err := e.svc.Observe(bg(), "conv_nope", chat.Turn{}, e.runner.Play(nil)); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("Observe on an unknown conversation err = %v", err)
	}
}

func TestObservedRunHoldsTheSlotAndTheLeaseHolderCanCancelIt(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	b := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "head")

	if err := e.svc.Observe(bg(), conv, chat.Turn{Text: "hold"}, e.runner.Play(chattest.DefaultScript(chat.Turn{Text: "hold"}))); err != nil {
		t.Fatal(err)
	}
	evs := s.until(deltaText("Working"))
	runID := evs[0].RunID
	if got := e.get(conv)["active_run"]; got != runID {
		t.Fatalf("active_run = %v, want the observed run %s", got, runID)
	}
	// Submit is refused with 409 exactly as for any active run, from HTTP and Go.
	e.wantCode(http.StatusConflict, "run_active", "POST", "/conversations/"+conv+"/turns", obj{"attachment": a, "text": "hi"})
	if _, err := e.svc.Submit(bg(), conv, chat.Turn{Text: "hi"}); !errors.Is(err, chat.ErrBusy) {
		t.Fatalf("Submit during an observed run err = %v", err)
	}
	// Nobody holds the lease yet, so nobody can cancel; the holder can.
	cancel := "/conversations/" + conv + "/runs/" + runID + "/cancel"
	e.wantCode(http.StatusForbidden, "lease_required", "POST", cancel, obj{"attachment": a})
	e.must(http.StatusOK, "POST", "/conversations/"+conv+"/lease", obj{"attachment": a})
	e.wantCode(http.StatusForbidden, "lease_required", "POST", cancel, obj{"attachment": b})
	e.must(http.StatusAccepted, "POST", cancel, obj{"attachment": a})
	evs = s.until(terminal)
	if last := evs[len(evs)-1]; last.Verb != chatstream.VerbRunAbort || last.RunID != runID {
		t.Fatalf("the canceled observed run ended with %s", last.Verb)
	}
	if n := len(e.runner.Turns()); n != 0 {
		t.Fatalf("%d refused submits reached the runner", n)
	}
}

func TestObserveWaitsForTheRunBeingPublished(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "head")
	first, _ := e.submit(conv, a, "hold")
	s.until(deltaText("Working"))

	// Bounded by its context while the other run is still active.
	ctx, cancel := context.WithCancel(bg())
	failed := make(chan error, 1)
	go func() {
		failed <- e.svc.Observe(ctx, conv, chat.Turn{Text: "gave up"}, e.runner.Play(chattest.Reply("never")))
	}()
	select {
	case err := <-failed:
		t.Fatalf("Observe returned %v while another run was active; it must wait", err)
	case <-time.After(50 * time.Millisecond): // it is still waiting, as it should be
	}
	cancel()
	select {
	case err := <-failed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Observe err = %v, want its context's", err)
		}
	case <-time.After(wait):
		t.Fatal("Observe did not return when its context ended")
	}

	// Waiting, it is admitted as soon as the first run's terminal event is out.
	adopted := make(chan error, 1)
	go func() {
		adopted <- e.svc.Observe(bg(), conv, chat.Turn{Text: "next turn"}, e.runner.Play(chattest.Reply("second")))
	}()
	select {
	case err := <-adopted:
		t.Fatalf("Observe returned %v before the first run ended", err)
	case <-time.After(50 * time.Millisecond):
	}
	e.runner.Release("hold")
	select {
	case err := <-adopted:
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
	case <-time.After(wait):
		t.Fatal("Observe was not admitted after the first run ended")
	}
	evs := s.until(func(ev chatstream.Event) bool { return ev.RunID != first && ev.IsTerminal() })
	// The second run starts strictly after the first one's terminal event.
	var order []string
	for _, ev := range evs {
		if ev.Verb == chatstream.VerbRunStart || ev.IsTerminal() {
			order = append(order, string(ev.Verb)+":"+map[bool]string{true: "first", false: "second"}[ev.RunID == first])
		}
	}
	if got := strings.Join(order, " "); got != "run.finish:first run.start:second run.finish:second" {
		t.Fatalf("run boundaries = %s", got)
	}
	if count(evs, func(ev chatstream.Event) bool {
		return ev.Role == "user" && ev.MetaString("origin") == chat.OriginRunner
	}) != 1 {
		t.Fatal("the abandoned Observe published something, or the admitted one did not publish its user message")
	}
}

// ---- generation: one authority ----

// external is a Control whose generation is bumped by its host, in the host's
// own transaction: it does not implement chat.Bumper.
type external struct{ chat.Control }

// sigRunner signals on an unbuffered channel, so a send returns only once the
// Service has taken the signal; a second send therefore returns only once the
// first has been handled.
type sigRunner struct {
	*chattest.Scripted
	ch chan string
}

func (r sigRunner) Replaced() <-chan string { return r.ch }

func TestGenerationIsReadFromAControlThatBumpsItself(t *testing.T) {
	inner := memstore.NewControl()
	ctl := external{Control: inner}
	if _, ok := chat.Control(ctl).(chat.Bumper); ok {
		t.Fatal("the test's Control must not be a Bumper")
	}
	runner := sigRunner{Scripted: chattest.NewScripted(), ch: make(chan string)}
	e := newEnv(t, withConfig(func(c *chat.Config) { c.Control, c.Runner = ctl, runner }))
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	watcher := e.attach(conv, chat.RoleViewer, chat.StreamDeltas)
	s := e.stream(conv, a, "0")
	runID, _ := e.submit(conv, a, "hold")
	s.until(deltaText("Working"))

	// A signal with no change in the Control does nothing at all.
	head := e.head(conv)
	runner.ch <- conv
	runner.ch <- conv
	if got := e.head(conv); got != head {
		t.Fatalf("a signal with no generation change published %d events", got-head)
	}
	if e.get(conv)["active_run"] != runID {
		t.Fatal("a signal with no generation change ended the run")
	}

	// The host bumps in its own store, then signals. Twice: the signal
	// carries no number and is safe to repeat.
	if _, err := inner.Bump(bg(), conv); err != nil {
		t.Fatal(err)
	}
	runner.ch <- conv
	runner.ch <- conv
	runner.ch <- conv
	tail := s.rest()
	if got := verbs(tail); got != "run.abort chat.generation" {
		t.Fatalf("after the host's bump the stream carried %q", got)
	}
	if tail[0].Reason != chat.AbortGeneration || value(t, tail[1])["generation"].(float64) != 2 {
		t.Fatalf("tail = %s", mustJSON(t, tail))
	}
	v := e.get(conv)
	if v["generation"].(float64) != 2 || v["active_run"] != nil || uint64(v["head"].(float64)) != tail[1].Seq {
		t.Fatalf("after the bump and three signals: %v", v)
	}
	out := e.wantCode(http.StatusConflict, "stale_attachment", "POST", "/conversations/"+conv+"/turns", obj{"attachment": a, "text": "x"})
	if out["generation"].(float64) != 2 {
		t.Fatalf("409 body = %v", out)
	}

	// The Service also notices a bump it was never signaled about, the next
	// time it reads the record: there is one number, and it is the Control's.
	if _, err := inner.Bump(bg(), conv); err != nil {
		t.Fatal(err)
	}
	if st, status, body := e.open(conv, watcher, "0", ""); st != nil || status != http.StatusConflict || body["generation"].(float64) != 3 {
		t.Fatalf("stream with an attachment two generations old = %d %v", status, body)
	}
	a3 := e.attach(conv, chat.RoleViewer, chat.StreamCommitted)
	hist := e.stream(conv, a3, "0").until(func(ev chatstream.Event) bool {
		return ev.Kind == chat.ActivityAttachment && value(t, ev)["attachment"] == a3
	})
	var gens []float64
	for _, ev := range hist {
		if ev.Kind == chat.ActivityGeneration {
			gens = append(gens, value(t, ev)["generation"].(float64))
		}
	}
	if len(gens) != 2 || gens[0] != 2 || gens[1] != 3 {
		t.Fatalf("chat.generation events = %v, want one for 2 and one for 3", gens)
	}
}

func TestARunLeftByADeadProcessIsEndedOnFirstTouch(t *testing.T) {
	// The store says a run is active, and no process is running it: this is a
	// host restart that cannot resume the run.
	ctl := memstore.NewControl()
	log := streamhub.NewMemoryLog()
	conv, _, err := ctl.Ensure(bg(), chat.NewConversation{ID: "conv_old", Agent: agentName, Owner: "owner-1", At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.BeginRun(bg(), conv.ID, "run_lost", "", time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, withConfig(func(c *chat.Config) { c.Control, c.Log = ctl, log }))
	v := e.get(conv.ID)
	if v["active_run"] != nil || v["generation"].(float64) != 2 {
		t.Fatalf("after a restart: %v", v)
	}
	a := e.attach(conv.ID, chat.RoleController, chat.StreamDeltas)
	evs := e.stream(conv.ID, a, "0").until(activityOf(chat.ActivityAttachment))
	if got := verbs(evs); got != "run.abort chat.generation chat.attachment" || evs[0].RunID != "run_lost" || evs[0].Reason != chat.AbortGeneration {
		t.Fatalf("log after a restart = %s", mustJSON(t, evs))
	}
	// And the conversation works again.
	next, _ := e.submit(conv.ID, a, "hello")
	if next == "" {
		t.Fatal("no run after recovery")
	}
}

// ---- runners that misbehave ----

type funcRunner struct {
	*chattest.Scripted
	start func(chat.Turn) (chat.Run, error)
}

func (r funcRunner) Start(_ context.Context, turn chat.Turn) (chat.Run, error) { return r.start(turn) }

type seqRun struct {
	events []chatstream.Event
	err    error
}

func (r seqRun) Cancel() {}
func (r seqRun) Answer(context.Context, string, chat.Decision) error {
	return errors.New("nothing to answer")
}
func (r seqRun) Events() iter.Seq2[chatstream.Event, error] {
	return func(yield func(chatstream.Event, error) bool) {
		for _, ev := range r.events {
			if !yield(ev, nil) {
				return
			}
		}
		if r.err != nil {
			yield(chatstream.Event{}, r.err)
		}
	}
}

func TestRunnerFailures(t *testing.T) {
	const marker = "what-is-on-the-desk"
	var logged bytes.Buffer
	var logMu sync.Mutex
	logger := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logged.Write(p)
	}), &slog.HandlerOptions{Level: slog.LevelDebug}))

	var next func(chat.Turn) (chat.Run, error)
	runner := funcRunner{Scripted: chattest.NewScripted(), start: func(t chat.Turn) (chat.Run, error) { return next(t) }}
	e := newEnv(t, withConfig(func(c *chat.Config) { c.Runner, c.Logger = runner, logger }))
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "0")
	s.until(activityOf(chat.ActivityAttachment))
	turns := "/conversations/" + conv + "/turns"
	body := func(text string) obj {
		return obj{"attachment": a, "text": text, "context": obj{"desk": marker}, "request_id": "req-" + text}
	}

	// The runner does not accept the turn: 502, and the turn did not happen.
	next = func(chat.Turn) (chat.Run, error) { return nil, errors.New("session is gone") }
	e.wantCode(http.StatusBadGateway, "runner_error", "POST", turns, body("refused"))
	if v := e.get(conv); v["active_run"] != nil {
		t.Fatalf("a refused start left the run slot taken: %v", v)
	}

	// The runner stops without a terminal event: the Service ends the run.
	next = func(chat.Turn) (chat.Run, error) {
		return seqRun{events: []chatstream.Event{
			{Verb: chatstream.VerbRunStart, Provider: "p"},
			{Verb: chatstream.VerbMessageStart, MessageID: "m", Role: "assistant"},
		}}, nil
	}
	lost, _ := e.submit(conv, a, "lost")
	evs := s.until(terminal)
	if last := evs[len(evs)-1]; last.Verb != chatstream.VerbRunError || last.Code != chatstream.CodeStreamLost || last.RunID != lost {
		t.Fatalf("a run with no terminal event ended with %s", mustJSON(t, last))
	}
	if count(evs, verb(chatstream.VerbRunStart)) != 1 {
		t.Fatalf("events = %s", verbs(evs))
	}

	// The runner's event sequence fails, before it even said run.start: the
	// stream still opens the run and shows the user's message.
	next = func(chat.Turn) (chat.Run, error) { return seqRun{err: errors.New("decoder broke")}, nil }
	broken, _ := e.submit(conv, a, "broken")
	evs = s.until(terminal)
	if got := verbs(evs); got != "run.start message.start part.start part.end message.end run.error" {
		t.Fatalf("a failed run = %s", got)
	}
	if last := evs[len(evs)-1]; last.Code != chat.CodeRunnerError || last.RunID != broken {
		t.Fatalf("run.error = %s", mustJSON(t, last))
	}

	// A second run.start and a terminal the runner emits twice change nothing.
	next = func(chat.Turn) (chat.Run, error) {
		return seqRun{events: []chatstream.Event{
			{Verb: chatstream.VerbRunStart}, {Verb: chatstream.VerbRunStart},
			{Verb: chatstream.VerbRunFinish, Reason: "stop"}, {Verb: chatstream.VerbRunFinish, Reason: "stop"},
		}}, nil
	}
	twice, _ := e.submit(conv, a, "twice")
	evs = s.until(terminal)
	wellFormed(t, ofRun(evs, twice))
	head := e.head(conv)
	if head != evs[len(evs)-1].Seq {
		t.Fatalf("events were published after the terminal event: head %d, terminal %d", head, evs[len(evs)-1].Seq)
	}

	logMu.Lock()
	defer logMu.Unlock()
	if out := logged.String(); strings.Contains(out, marker) || !strings.Contains(out, "session is gone") {
		t.Fatalf("log must carry the failure and never the context:\n%s", out)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

var _ io.Writer = writerFunc(nil)

func TestServiceCloseEndsActiveRuns(t *testing.T) {
	ctl := memstore.NewControl()
	log := streamhub.NewMemoryLog()
	runner := chattest.NewScripted()
	svc, err := chat.NewService(chat.Config{
		Control: ctl, Log: log, Runner: runner, NewID: chattest.IDs(),
		Agents: func(context.Context, string) (chat.Agent, error) { return chat.Agent{Owner: "o"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := svc.EnsureConversation(bg(), "any")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.Submit(bg(), v.ConversationID, chat.Turn{Text: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(bg(), wait)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	var last streamhub.Record
	for rec, err := range log.After(bg(), v.ConversationID, 0) {
		if err != nil {
			t.Fatal(err)
		}
		last = rec
	}
	if last.Name != string(chatstream.VerbRunAbort) || !strings.Contains(string(last.Data), receipt.RunID) ||
		!strings.Contains(string(last.Data), chat.AbortShutdown) {
		t.Fatalf("last record after Close = %s %s", last.Name, last.Data)
	}
	if rec, _ := ctl.Get(bg(), v.ConversationID); rec.ActiveRun != "" {
		t.Fatalf("the store still says run %q is active", rec.ActiveRun)
	}
	if _, err := svc.Conversation(bg(), v.ConversationID); !errors.Is(err, chat.ErrServiceClosed) {
		t.Fatalf("use after Close err = %v", err)
	}
}

func TestNewServiceAndHandlerRequireTheirPorts(t *testing.T) {
	agents := func(context.Context, string) (chat.Agent, error) { return chat.Agent{}, nil }
	for name, cfg := range map[string]chat.Config{
		"no agents":  {Log: streamhub.NewMemoryLog(), Control: memstore.NewControl()},
		"no log":     {Agents: agents, Control: memstore.NewControl()},
		"no control": {Agents: agents, Log: streamhub.NewMemoryLog()},
	} {
		if _, err := chat.NewService(cfg); err == nil {
			t.Errorf("%s: NewService succeeded", name)
		}
	}
	if _, err := chat.NewHandler(nil, nil); err == nil {
		t.Error("NewHandler(nil, nil) succeeded")
	}
	// An agent with no runner anywhere cannot be served.
	svc, err := chat.NewService(chat.Config{Agents: agents, Log: streamhub.NewMemoryLog(), Control: memstore.NewControl()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.Close(bg()) }()
	if _, err := svc.EnsureConversation(bg(), "a"); err == nil {
		t.Error("EnsureConversation succeeded for an agent with no runner")
	}
}
