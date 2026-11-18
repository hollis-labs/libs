package chat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
	"github.com/hollis-labs/libs/agent/chat/chattest"
	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
)

func TestSubmitPublishesTheRunWithTheUsersMessageFirst(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	if again := e.conversation(); again != conv {
		t.Fatalf("the agent's conversation changed between calls: %s then %s", conv, again)
	}
	att := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, att, "0")

	out := e.must(http.StatusAccepted, "POST", "/conversations/"+conv+"/turns", obj{
		"attachment": att, "text": "hello world", "request_id": "req-1",
		"meta": obj{"auto": true}, "context": obj{"desk": []string{"a"}},
	})
	runID, cursor := out["run_id"].(string), uint64(out["cursor"].(float64))

	evs := s.until(terminal)
	contiguous(t, evs, 1)
	if got := verbs(evs[:2]); got != "chat.attachment chat.lease" {
		t.Fatalf("conversation-level events = %s", got)
	}
	for _, ev := range evs[:2] {
		if ev.RunID != "" {
			t.Errorf("%s carries run_id %q; conversation-level activity carries none", ev.Kind, ev.RunID)
		}
	}
	if v := value(t, evs[1]); v["state"] != "acquired" || v["attachment"] != att {
		t.Errorf("chat.lease = %v", v)
	}
	if cursor != evs[1].Seq {
		t.Errorf("cursor = %d, want the head when the turn was admitted (%d)", cursor, evs[1].Seq)
	}

	run := evs[2:]
	for _, ev := range run {
		if ev.RunID != runID {
			t.Fatalf("%s (seq %d) has run_id %q, want %q", ev.Verb, ev.Seq, ev.RunID, runID)
		}
		if ev.Seq <= cursor {
			t.Fatalf("run event seq %d is not after the cursor %d", ev.Seq, cursor)
		}
	}
	wellFormed(t, run)
	want := "run.start message.start part.start part.end message.end " +
		"message.start part.start part.delta part.delta part.delta part.delta part.end message.end run.finish"
	if got := verbs(run); got != want {
		t.Fatalf("run = %s\nwant  %s", got, want)
	}
	if run[0].Provider != "scripted" || run[0].Model != "script-1" {
		t.Errorf("run.start = provider %q, model %q; the runner's own were lost", run[0].Provider, run[0].Model)
	}
	user := run[1]
	if user.Role != "user" || user.MetaString("request_id") != "req-1" || !user.MetaBool("auto") {
		t.Errorf("user message.start = role %q, meta %s", user.Role, mustJSON(t, user.Meta))
	}
	if got := finalText(t, run[3]); got != "hello world" {
		t.Errorf("user text = %q", got)
	}
	if strings.Contains(string(mustJSON(t, evs)), "desk") {
		t.Error("the turn's context was published")
	}
	if got := streamedText(run)[runID+"/p1"]; got != "You said: hello world" {
		t.Errorf("streamed answer = %q", got)
	}
	if got := finalText(t, run[11]); got != "You said: hello world" {
		t.Errorf("committed answer = %q", got)
	}

	// The runner was handed the turn, with the context the stream never saw.
	turns := e.runner.Turns()
	if len(turns) != 1 || turns[0].RunID != runID || turns[0].ConversationID != conv || turns[0].Text != "hello world" ||
		string(turns[0].Context) != `{"desk":["a"]}` {
		t.Fatalf("runner turns = %d, first run %q", len(turns), turns[0].RunID)
	}

	v := e.get(conv)
	if v["active_run"] != nil || uint64(v["head"].(float64)) != evs[len(evs)-1].Seq || v["generation"].(float64) != 1 {
		t.Errorf("conversation after the run = %v", v)
	}
	if l, _ := v["lease"].(obj); l == nil || l["attachment"] != att {
		t.Errorf("lease = %v", v["lease"])
	}
	if c := v["capabilities"].(obj); c["deltas"] != true || c["approvals"] != true || c["always"] != false {
		t.Errorf("capabilities = %v", c)
	}
	if v["agent"] != agentName || v["owner"] != "owner-1" || v["scope"] != "seat-1" || v["kind"] != "stable" ||
		v["runner_ref"] != "scripted:"+conv {
		t.Errorf("record = %v", v)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRunIDIsTheServicesOnEveryRunnerEvent(t *testing.T) {
	script := func(chat.Turn) []chattest.Step {
		steps := chattest.Join(
			chattest.Reply("one ", "two"),
		)
		for i := range steps {
			steps[i].Event.RunID = "decoder-run-7" // a decoder's own id must not leak
		}
		return steps
	}
	e := newEnv(t, withRunner(chattest.WithScript(script)))
	conv := e.conversation()
	att := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, att, "0")
	runID, _ := e.submit(conv, att, "go")
	evs := s.until(terminal)
	run := ofRun(evs, runID)
	if len(run) != len(evs)-2 {
		t.Fatalf("%d of %d events carry the Service's run_id %q: %s", len(run), len(evs)-2, runID, mustJSON(t, evs))
	}
	if strings.Contains(string(mustJSON(t, evs)), "decoder-run-7") {
		t.Fatal("the runner's own run_id reached the stream")
	}
	wellFormed(t, run)
}

func TestLeaseContentionExpiryAndTakeover(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	b := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	v := e.attach(conv, chat.RoleViewer, chat.StreamCommitted)
	s := e.stream(conv, v, "head")
	turns := "/conversations/" + conv + "/turns"
	lease := "/conversations/" + conv + "/lease"

	// A viewer can never type.
	e.wantCode(http.StatusForbidden, "lease_required", "POST", turns, obj{"attachment": v, "text": "hi"})
	e.wantCode(http.StatusForbidden, "not_controller", "POST", lease, obj{"attachment": v})

	// A's submit acquires the free lease.
	runA, _ := e.submit(conv, a, "first")
	evs := s.until(func(ev chatstream.Event) bool { return ev.RunID == runA && ev.IsTerminal() })
	e.wantCode(http.StatusConflict, "lease_held", "POST", lease, obj{"attachment": b})
	e.wantCode(http.StatusForbidden, "lease_required", "POST", turns, obj{"attachment": b, "text": "mine"})

	// A renews at 60s; at 91s from the first acquire the lease still stands.
	e.clock.Advance(60 * time.Second)
	renewed := e.must(http.StatusOK, "POST", lease, obj{"attachment": a})
	if renewed["attachment"] != a {
		t.Fatalf("renewal = %v", renewed)
	}
	e.clock.Advance(31 * time.Second)
	e.wantCode(http.StatusConflict, "lease_held", "POST", lease, obj{"attachment": b})
	// Not renewed again: it lapses 90s after the renewal.
	e.clock.Advance(59 * time.Second)
	if e.get(conv)["lease"] != nil {
		t.Fatal("the lease is still reported after its expiry")
	}
	e.must(http.StatusOK, "POST", lease, obj{"attachment": b})
	e.wantCode(http.StatusForbidden, "lease_required", "POST", turns, obj{"attachment": a, "text": "too late"})

	// Takeover needs its own permission, and replaces the lease at once.
	e.deny(chat.PermTakeover, true)
	e.wantCode(http.StatusForbidden, "forbidden", "POST", lease+"/takeover", obj{"attachment": a})
	e.deny(chat.PermTakeover, false)
	took := e.must(http.StatusOK, "POST", lease+"/takeover", obj{"attachment": a})
	if took["attachment"] != a {
		t.Fatalf("takeover = %v", took)
	}
	e.wantCode(http.StatusForbidden, "lease_required", "POST", turns, obj{"attachment": b, "text": "displaced"})
	runA2, _ := e.submit(conv, a, "second")

	evs = append(evs, s.until(func(ev chatstream.Event) bool { return ev.RunID == runA2 && ev.IsTerminal() })...)
	var leases []string
	for _, ev := range evs {
		if activityOf(chat.ActivityLease)(ev) {
			v := value(t, ev)
			prev, _ := v["previous"].(string)
			leases = append(leases, v["state"].(string)+":"+v["attachment"].(string)+":"+prev)
		}
	}
	// A's submit acquired it; B acquired after A's expiry (nobody displaced);
	// then A took it from B.
	want := []string{"acquired:" + a + ":", "acquired:" + b + ":", "acquired:" + a + ":" + b}
	if strings.Join(leases, ",") != strings.Join(want, ",") {
		t.Fatalf("chat.lease events = %v, want %v", leases, want)
	}
	if n := len(e.runner.Turns()); n != 2 {
		t.Fatalf("the runner started %d turns, want 2: a refused submit reached it", n)
	}
}

func TestSubmitDuringAnActiveRunIs409AndCancelEndsWithRunAbort(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	b := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "0")
	turns := "/conversations/" + conv + "/turns"

	runID, _ := e.submit(conv, a, "hold on")
	s.until(deltaText("Working"))
	if got := e.get(conv)["active_run"]; got != runID {
		t.Fatalf("active_run = %v, want %s", got, runID)
	}
	e.wantCode(http.StatusConflict, "run_active", "POST", turns, obj{"attachment": a, "text": "again"})

	cancel := "/conversations/" + conv + "/runs/" + runID + "/cancel"
	e.wantCode(http.StatusForbidden, "lease_required", "POST", cancel, obj{"attachment": b})
	e.wantCode(http.StatusConflict, "run_not_active", "POST", "/conversations/"+conv+"/runs/run_nope/cancel", obj{"attachment": a})
	e.wantCode(http.StatusBadRequest, "invalid_request", "POST", cancel, nil)
	e.must(http.StatusAccepted, "POST", cancel, obj{"attachment": a})

	evs := s.until(terminal)
	last := evs[len(evs)-1]
	if last.Verb != chatstream.VerbRunAbort || last.RunID != runID {
		t.Fatalf("the canceled run ended with %s (%s)", last.Verb, last.Reason)
	}
	// The runner closed what it had open, and the Service filled final from
	// the deltas it had passed on.
	end := evs[len(evs)-3]
	if end.Verb != chatstream.VerbPartEnd || finalText(t, end) != "Working" {
		t.Fatalf("part.end before the abort = %s %s", end.Verb, end.Final)
	}
	if e.clock.Pending() != 0 {
		t.Errorf("%d timers left after the run ended", e.clock.Pending())
	}
	if e.get(conv)["active_run"] != nil {
		t.Fatal("active_run still set after run.abort")
	}
	next, _ := e.submit(conv, a, "after cancel")
	s.until(func(ev chatstream.Event) bool { return ev.RunID == next && ev.Verb == chatstream.VerbRunFinish })
}

// stuck is a runner whose run ignores Cancel until the test lets go.
type stuck struct{ *chattest.Scripted }

type stuckRun struct{ chat.Run }

func (r stuckRun) Cancel() {} // deaf

func (s stuck) Start(ctx context.Context, turn chat.Turn) (chat.Run, error) {
	run, err := s.Scripted.Start(ctx, turn)
	return stuckRun{Run: run}, err
}

func TestACanceledRunThatDoesNotStopIsEndedAfterTheGrace(t *testing.T) {
	inner := chattest.NewScripted()
	e := newEnv(t, withConfig(func(c *chat.Config) { c.Runner = stuck{Scripted: inner} }))
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "0")
	runID, _ := e.submit(conv, a, "hold")
	s.until(deltaText("Working"))
	e.must(http.StatusAccepted, "POST", "/conversations/"+conv+"/runs/"+runID+"/cancel", obj{"attachment": a})
	e.clock.Advance(chat.DefaultCancelGrace)
	evs := s.until(terminal)
	if last := evs[len(evs)-1]; last.Verb != chatstream.VerbRunAbort || last.Reason != chat.AbortCanceled {
		t.Fatalf("ended with %s (%s)", last.Verb, last.Reason)
	}
	// The slot is free even though the runner never stopped; what it emits
	// later is not published.
	head := e.head(conv)
	inner.Release("hold")
	next, _ := e.submit(conv, a, "next")
	evs = s.until(func(ev chatstream.Event) bool { return ev.RunID == next && ev.IsTerminal() })
	if n := len(ofRun(evs, runID)); n != 0 {
		t.Fatalf("%d events of the ended run were published after its run.abort", n)
	}
	if evs[0].Seq != head+1 {
		t.Fatalf("first event after the abort has seq %d, want %d", evs[0].Seq, head+1)
	}
}

type grants struct {
	mu  sync.Mutex
	got []chat.Grant
}

func (g *grants) Grant(_ context.Context, gr chat.Grant) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.got = append(g.got, gr)
	return nil
}

func TestApprovalAnswered(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	b := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "0")
	runID, _ := e.submit(conv, a, "approval please")
	evs := s.until(verb(chatstream.VerbApprovalRequest))
	req := evs[len(evs)-1]
	if req.Mode != chatstream.ApprovalInBand || req.ApprovalID != "ap1" || req.RunID != runID || req.ExpiresAt == nil ||
		!req.ExpiresAt.Equal(e.clock.Now().Add(chat.DefaultApprovalTTL)) {
		t.Fatalf("approval.request = %s", mustJSON(t, req))
	}
	var d struct {
		Description string `json:"description"`
		Standing    bool   `json:"standing"`
	}
	if err := json.Unmarshal(req.Descriptor, &d); err != nil || d.Description != "Write notes.txt" || !d.Standing {
		t.Fatalf("descriptor = %s", req.Descriptor)
	}
	pending := e.get(conv)["pending_approvals"].([]any)
	if len(pending) != 1 || pending[0].(obj)["approval_id"] != "ap1" || pending[0].(obj)["run_id"] != runID {
		t.Fatalf("pending_approvals = %v", pending)
	}

	path := "/conversations/" + conv + "/approvals/ap1"
	e.wantCode(http.StatusForbidden, "lease_required", "POST", path, obj{"attachment": b, "decision": "allow"})
	e.wantCode(http.StatusBadRequest, "invalid_request", "POST", path, obj{"attachment": a, "decision": "maybe"})
	e.wantCode(http.StatusNotFound, "unknown_approval", "POST", "/conversations/"+conv+"/approvals/nope", obj{"attachment": a, "decision": "allow"})
	// No Grants store: "always" is not offered, and the approval stays pending.
	e.wantCode(http.StatusUnprocessableEntity, "scope_unavailable", "POST", path, obj{"attachment": a, "decision": "allow", "scope": "always"})
	e.must(http.StatusNoContent, "POST", path, obj{"attachment": a, "decision": "allow", "scope": "conversation"})
	// First answer wins.
	e.wantCode(http.StatusConflict, "approval_resolved", "POST", path, obj{"attachment": a, "decision": "deny"})

	evs = s.until(terminal)
	if evs[0].Kind != chat.ActivityApproval || evs[0].RunID != runID || evs[0].ApprovalID != "ap1" {
		t.Fatalf("after the answer came %s, want chat.approval of the run", verbs(evs[:1]))
	}
	if v := value(t, evs[0]); v["decision"] != "allow" || v["scope"] != "conversation" || v["attachment"] != a || v["approval_id"] != "ap1" {
		t.Fatalf("chat.approval = %v", v)
	}
	if count(evs, activityOf(chat.ActivityApproval)) != 1 {
		t.Fatal("more than one chat.approval for one approval")
	}
	msgs := committed(t, ofRun(evs, runID))
	if got := msgs[len(msgs)-1].Parts; len(got) != 2 || got[0] != "tool_result:written" || got[1] != "text:Done: the file is written." {
		t.Fatalf("after allow the run said %v", got)
	}
	if evs[len(evs)-1].Verb != chatstream.VerbRunFinish {
		t.Fatalf("run ended with %s", evs[len(evs)-1].Verb)
	}
	if e.get(conv)["pending_approvals"] != nil || e.clock.Pending() != 0 {
		t.Fatal("the approval is still pending, or its timer still set, after the run")
	}
}

func TestApprovalExpiresAndIsDenied(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "0")
	runID, _ := e.submit(conv, a, "approval")
	s.until(verb(chatstream.VerbApprovalRequest))

	e.clock.Advance(chat.DefaultApprovalTTL - time.Second)
	if e.get(conv)["pending_approvals"] == nil {
		t.Fatal("the approval was resolved before its expires_at")
	}
	e.clock.Advance(time.Second)
	evs := s.until(terminal)
	v := value(t, evs[0])
	if evs[0].Kind != chat.ActivityApproval || v["decision"] != "deny" || v["reason"] != chat.ReasonExpired || v["attachment"] != nil {
		t.Fatalf("expiry outcome = %s %v", evs[0].Kind, v)
	}
	msgs := committed(t, ofRun(evs, runID))
	if got := msgs[len(msgs)-1].Parts; got[0] != "tool_result:permission denied" {
		t.Fatalf("after expiry the run said %v", got)
	}
	// Ten minutes on, the controller's own lease has lapsed too; with it back,
	// an answer that comes after the expiry is told it lost.
	path := "/conversations/" + conv + "/approvals/ap1"
	e.wantCode(http.StatusForbidden, "lease_required", "POST", path, obj{"attachment": a, "decision": "allow"})
	e.must(http.StatusOK, "POST", "/conversations/"+conv+"/lease", obj{"attachment": a})
	e.wantCode(http.StatusConflict, "approval_resolved", "POST", path, obj{"attachment": a, "decision": "allow"})
}

func TestApprovalWaitsForAControllerWhoAcquiresTheLeaseLater(t *testing.T) {
	g := &grants{}
	e := newEnv(t, withConfig(func(c *chat.Config) { c.Grants = g }))
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	viewer := e.attach(conv, chat.RoleViewer, chat.StreamCommitted)
	s := e.stream(conv, viewer, "0")
	runID, _ := e.submit(conv, a, "approval")
	s.until(verb(chatstream.VerbApprovalRequest))

	// The controller leaves. The lease is released, the run and its approval wait.
	e.must(http.StatusNoContent, "DELETE", "/conversations/"+conv+"/attachments/"+a, nil)
	v := e.get(conv)
	if v["lease"] != nil || v["active_run"] != runID || len(v["pending_approvals"].([]any)) != 1 {
		t.Fatalf("after the controller left: %v", v)
	}
	e.clock.Advance(5 * time.Minute)

	b := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	path := "/conversations/" + conv + "/approvals/ap1"
	e.wantCode(http.StatusForbidden, "lease_required", "POST", path, obj{"attachment": b, "decision": "allow"})
	e.must(http.StatusOK, "POST", "/conversations/"+conv+"/lease", obj{"attachment": b})
	e.must(http.StatusNoContent, "POST", path, obj{"attachment": b, "decision": "allow", "scope": "always"})

	evs := s.until(terminal)
	if evs[len(evs)-1].Verb != chatstream.VerbRunFinish {
		t.Fatalf("run ended with %s", evs[len(evs)-1].Verb)
	}
	if !e.get(conv)["capabilities"].(obj)["always"].(bool) {
		t.Error("capabilities.always is false with a Grants store")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.got) != 1 || g.got[0].ApprovalID != "ap1" || g.got[0].ConversationID != conv || g.got[0].CallID != "c1" ||
		g.got[0].Agent != agentName || g.got[0].Owner != "owner-1" || g.got[0].RunID != runID || len(g.got[0].Descriptor) == 0 {
		t.Fatalf("standing grants = %+v", g.got)
	}
}

func TestAnApprovalAskedEveryTimeTakesOnlyOnce(t *testing.T) {
	e := newEnv(t, withConfig(func(c *chat.Config) { c.Grants = &grants{} }))
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "0")
	e.submit(conv, a, "confirm")
	s.until(verb(chatstream.VerbApprovalRequest))
	path := "/conversations/" + conv + "/approvals/ap1"
	for _, scope := range []string{"conversation", "always"} {
		e.wantCode(http.StatusUnprocessableEntity, "scope_unavailable", "POST", path, obj{"attachment": a, "decision": "allow", "scope": scope})
	}
	e.must(http.StatusNoContent, "POST", path, obj{"attachment": a, "decision": "deny"})
	evs := s.until(terminal)
	if v := value(t, evs[0]); v["decision"] != "deny" || v["scope"] != "once" {
		t.Fatalf("chat.approval = %v", v)
	}
}

func TestGenerationBump(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "0")
	runID, _ := e.submit(conv, a, "hold")
	before := s.until(deltaText("Working"))

	e.runner.Replace(conv)
	tail := s.rest() // an open stream ends after chat.generation
	if got := verbs(tail); got != "run.abort chat.generation" {
		t.Fatalf("after the replacement the stream carried %q", got)
	}
	if tail[0].RunID != runID || tail[0].Reason != chat.AbortGeneration {
		t.Fatalf("run.abort = run %q, reason %q", tail[0].RunID, tail[0].Reason)
	}
	if v := value(t, tail[1]); v["generation"].(float64) != 2 || tail[1].RunID != "" {
		t.Fatalf("chat.generation = %v (run_id %q)", v, tail[1].RunID)
	}
	all := append(before, tail...)
	contiguous(t, all, 1)
	cursor := s.last

	// The conversation ID, the log and every cursor are intact.
	v := e.get(conv)
	if v["conversation_id"] != conv || v["generation"].(float64) != 2 || v["active_run"] != nil || v["lease"] != nil ||
		uint64(v["head"].(float64)) != tail[1].Seq {
		t.Fatalf("after the bump: %v", v)
	}

	// Every request with the old attachment is 409 stale_attachment and
	// carries the current generation.
	c := "/conversations/" + conv
	for _, r := range []struct {
		method, path string
		body         obj
	}{
		{"POST", c + "/turns", obj{"attachment": a, "text": "hi"}},
		{"POST", c + "/lease", obj{"attachment": a}},
		{"POST", c + "/lease/takeover", obj{"attachment": a}},
		{"POST", c + "/runs/" + runID + "/cancel", obj{"attachment": a}},
		{"POST", c + "/approvals/ap1", obj{"attachment": a, "decision": "allow"}},
		{"DELETE", c + "/attachments/" + a, nil},
	} {
		out := e.wantCode(http.StatusConflict, "stale_attachment", r.method, r.path, r.body)
		if out["generation"].(float64) != 2 {
			t.Fatalf("%s %s: body %v does not carry generation 2", r.method, r.path, out)
		}
	}
	// A stream opened with a stale attachment is answered, never held open.
	if st, status, body := e.open(conv, a, "", cursor); st != nil || status != http.StatusConflict ||
		body["code"] != "stale_attachment" || body["generation"].(float64) != 2 {
		t.Fatalf("stale stream = %d %v", status, body)
	}

	// The client attaches again and resumes with its cursor: nothing twice.
	a2 := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s2 := e.resume(conv, a2, cursor)
	next, _ := e.submit(conv, a2, "back again") // whoever re-acquires first holds the lease
	resumed := s2.until(func(ev chatstream.Event) bool { return ev.RunID == next && ev.IsTerminal() })
	contiguous(t, resumed, tail[1].Seq+1)
	if got := verbs(resumed[:2]); got != "chat.attachment chat.lease" {
		t.Fatalf("resumed stream began with %q", got)
	}

	// Full history is still there from 0, generation event included, and a
	// new attachment's stream does not end at an old chat.generation.
	s3 := e.stream(conv, a2, "0")
	history := s3.until(func(ev chatstream.Event) bool { return ev.RunID == next && ev.IsTerminal() })
	increasing(t, history)
	if count(history, activityOf(chat.ActivityGeneration)) != 1 || count(history, verb(chatstream.VerbPartDelta)) != 0 {
		t.Fatalf("history = %s", verbs(history))
	}
}

func TestReset(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	b := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, b, "0")
	reset := "/agents/" + agentName + "/conversation/reset"

	runID, _ := e.submit(conv, a, "hold")
	s.until(deltaText("Working"))
	e.wantCode(http.StatusConflict, "run_active", "POST", reset, obj{"attachment": a})
	e.runner.Release("hold")
	s.until(func(ev chatstream.Event) bool { return ev.RunID == runID && ev.IsTerminal() })

	// Lease holder only, while somebody holds it.
	e.wantCode(http.StatusForbidden, "lease_required", "POST", reset, obj{"attachment": b})
	e.wantCode(http.StatusForbidden, "lease_required", "POST", reset, nil)
	e.deny(chat.PermControl, true)
	e.wantCode(http.StatusForbidden, "forbidden", "POST", reset, obj{"attachment": a})
	e.deny(chat.PermControl, false)

	fresh := e.must(http.StatusOK, "POST", reset, obj{"attachment": a})
	next := fresh["conversation_id"].(string)
	if next == conv || fresh["generation"].(float64) != 1 || fresh["head"].(float64) != 0 || fresh["lease"] != nil {
		t.Fatalf("new conversation = %v", fresh)
	}
	if e.conversation() != next {
		t.Fatal("the agent's current conversation is not the new one")
	}

	// The open stream ends with chat.closed naming the successor.
	tail := s.rest()
	last := tail[len(tail)-1]
	if v := value(t, last); last.Kind != chat.ActivityClosed || v["code"] != "conversation_closed" || v["conversation_id"] != next {
		t.Fatalf("stream ended with %s %s", last.Kind, last.Value)
	}

	// Every request on the old conversation is 410 with the new ID.
	c := "/conversations/" + conv
	for _, r := range []struct {
		method, path string
		body         obj
	}{
		{"GET", c, nil},
		{"POST", c + "/attachments", obj{"role": "viewer", "stream": "deltas"}},
		{"DELETE", c + "/attachments/" + a, nil},
		{"POST", c + "/lease", obj{"attachment": a}},
		{"POST", c + "/lease/takeover", obj{"attachment": a}},
		{"POST", c + "/turns", obj{"attachment": a, "text": "hi"}},
		{"POST", c + "/runs/" + runID + "/cancel", obj{"attachment": a}},
		{"POST", c + "/approvals/ap1", obj{"attachment": a, "decision": "allow"}},
	} {
		out := e.wantCode(http.StatusGone, "conversation_closed", r.method, r.path, r.body)
		if out["conversation_id"] != next {
			t.Fatalf("%s %s: body %v does not carry the new ID", r.method, r.path, out)
		}
	}
	if st, status, body := e.open(conv, a, "0", ""); st != nil || status != http.StatusGone || body["conversation_id"] != next {
		t.Fatalf("stream on the closed conversation = %d %v", status, body)
	}
	// The old log is kept.
	if h, err := e.log.Head(context.Background(), conv); err != nil || uint64(h.Latest) != last.Seq {
		t.Fatalf("old log head = %+v, %v; want %d", h, err, last.Seq)
	}

	// With nobody holding the lease, anyone with control may reset.
	again := e.must(http.StatusOK, "POST", reset, nil)
	if again["conversation_id"] == next {
		t.Fatal("reset with a free lease did not replace the conversation")
	}
}

func TestReconnectMidMessageLosesNothingAndRepeatsNothing(t *testing.T) {
	script := func(chat.Turn) []chattest.Step {
		return chattest.Join(
			chattest.Start("scripted", "m"),
			[]chattest.Step{
				{Event: chatstream.Event{Verb: chatstream.VerbMessageStart, MessageID: "m1", Role: "assistant"}},
				{Event: chatstream.Event{Verb: chatstream.VerbPartStart, PartID: "p1", Kind: "text"}},
				{Event: chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: "p1", Text: "a"}},
				{Event: chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: "p1", Text: "b"}},
			},
			chattest.Gate("mid"),
			[]chattest.Step{
				{Event: chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: "p1", Text: "c"}},
				{Event: chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: "p1", Text: "d"}},
			},
			chattest.Gate("late"),
			[]chattest.Step{
				{Event: chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: "p1", Text: "e"}},
				{Event: chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: "p1"}}, // no final: the Service fills it
				{Event: chatstream.Event{Verb: chatstream.VerbMessageEnd, MessageID: "m1"}},
			},
			chattest.Finish(chatstream.FinishStop),
		)
	}
	e := newEnv(t, withRunner(chattest.WithScript(script)))
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "0")
	runID, _ := e.submit(conv, a, "go")

	got := s.until(deltaText("a"))
	cursor := s.last
	s.close() // dropped in the middle of the message, with "b" not yet read

	// Events keep arriving while nobody is connected.
	e.runner.Release("mid")
	watcher := e.stream(conv, e.attach(conv, chat.RoleViewer, chat.StreamDeltas), cursor)
	watcher.until(deltaText("d"))

	// Reconnect while the run is still in progress: replay includes its deltas.
	s = e.resume(conv, a, cursor)
	got = append(got, s.until(deltaText("d"))...)
	cursor = s.last
	s.close()
	e.runner.Release("late")
	watcher.until(terminal)

	// Reconnect after the run finished: it is history now, sent without
	// deltas, and final makes up for the delta that was never delivered.
	s = e.resume(conv, a, cursor)
	rest := s.until(terminal)
	if count(rest, verb(chatstream.VerbPartDelta)) != 0 {
		t.Fatalf("a finished run was replayed with deltas: %s", verbs(rest))
	}
	got = append(got, rest...)

	increasing(t, got)
	run := ofRun(got, runID)
	if text := streamedText(run)[runID+"/p1"]; text != "abcd" {
		t.Fatalf("deltas received across the reconnects = %q, want abcd with none lost or repeated", text)
	}
	var end chatstream.Event
	for _, ev := range run {
		if ev.Verb == chatstream.VerbPartEnd && ev.PartID == "p1" {
			end = ev
		}
	}
	if finalText(t, end) != "abcde" {
		t.Fatalf("final = %s, want the whole text", end.Final)
	}
	// While the run was live nothing at all was skipped.
	contiguous(t, got[:len(got)-len(rest)], 1)
}

func TestCommittedOnlyStreamAndHistoryWithoutDeltas(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	viewer := e.attach(conv, chat.RoleViewer, chat.StreamCommitted)
	live := e.stream(conv, a, "0")
	com := e.stream(conv, viewer, "0")

	run1, _ := e.submit(conv, a, "tool")
	full1 := live.until(terminal)
	run2, _ := e.submit(conv, a, "hold")
	live.until(deltaText("Working"))

	// Committed-only: every event except part.delta, with the same cursors.
	comEvs := com.until(func(ev chatstream.Event) bool {
		return ev.RunID == run2 && ev.Verb == chatstream.VerbPartStart && ev.PartID == "p1"
	})
	if n := count(comEvs, verb(chatstream.VerbPartDelta)); n != 0 {
		t.Fatalf("committed-only stream carried %d part.delta", n)
	}
	increasing(t, comEvs)
	var want []chatstream.Event
	for _, ev := range full1 {
		if ev.Verb != chatstream.VerbPartDelta {
			want = append(want, ev)
		}
	}
	if string(mustJSON(t, comEvs[:len(want)])) != string(mustJSON(t, want)) {
		t.Fatalf("committed-only stream differs from the full one by more than its deltas:\n%s\n%s", verbs(comEvs), verbs(want))
	}
	// It builds every message from part.start and part.end alone.
	msgs := committed(t, ofRun(comEvs, run1))
	wantMsgs := [][]string{
		{"text:tool"},
		{"text:Let me look that up.", `tool_call:{"query":"everything"}`},
		{"tool_result:42 results", "text:I found 42 results."},
	}
	if string(mustJSON(t, partsOf(msgs))) != string(mustJSON(t, wantMsgs)) {
		t.Fatalf("committed messages = %s", mustJSON(t, partsOf(msgs)))
	}

	// A deltas client that starts now gets history without deltas for the run
	// that had finished, and the run in progress in full.
	late := e.attach(conv, chat.RoleViewer, chat.StreamDeltas)
	ls := e.stream(conv, late, "0")
	hist := ls.until(deltaText("Working"))
	increasing(t, hist)
	if n := count(ofRun(hist, run1), verb(chatstream.VerbPartDelta)); n != 0 {
		t.Fatalf("history of the finished run carried %d part.delta", n)
	}
	if n := count(ofRun(hist, run2), verb(chatstream.VerbPartDelta)); n != 1 {
		t.Fatalf("the run in progress was replayed with %d part.delta, want 1", n)
	}
	if got := committed(t, ofRun(hist, run1)); string(mustJSON(t, partsOf(got))) != string(mustJSON(t, wantMsgs)) {
		t.Fatalf("history messages = %s", mustJSON(t, partsOf(got)))
	}

	// A committed-only client's cursor is the seq of the last event it was
	// sent: switching to deltas from it neither repeats nor skips.
	cursor := com.last
	com.close()
	e.runner.Release("hold")
	live.until(terminal)
	switched := e.resume(conv, late, cursor)
	tail := switched.until(terminal)
	n, _ := strconv.ParseUint(cursor, 10, 64)
	if tail[0].Seq <= n {
		t.Fatalf("resume from %d began at seq %d", n, tail[0].Seq)
	}
	msgs = committed(t, append(comEvs, tail...))
	if got := msgs[len(msgs)-1].Parts; len(got) != 1 || got[0] != "text:Working and done." {
		t.Fatalf("last message across the mode switch = %v", got)
	}
}

func partsOf(msgs []message) [][]string {
	out := make([][]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Parts
	}
	return out
}

func TestWholeMessageRunner(t *testing.T) {
	e := newEnv(t, withRunner(chattest.Whole(), chattest.WithoutApprovals()))
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "0")
	if c := e.get(conv)["capabilities"].(obj); c["deltas"] != false || c["approvals"] != false {
		t.Fatalf("capabilities = %v", c)
	}
	runID, _ := e.submit(conv, a, "reasoning")
	evs := s.until(terminal)
	if n := count(evs, verb(chatstream.VerbPartDelta)); n != 0 {
		t.Fatalf("a deltas client got %d part.delta from a runner that does not stream", n)
	}
	wellFormed(t, ofRun(evs, runID))
	msgs := committed(t, ofRun(evs, runID))
	if got := msgs[1].Parts; len(got) != 2 || got[0] != "reasoning:The user wants an answer." || got[1] != "text:Here it is." {
		t.Fatalf("message = %v", got)
	}
}

func TestRequestIDMakesARetrySafe(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "0")
	turns := "/conversations/" + conv + "/turns"
	body := obj{"attachment": a, "text": "hold", "request_id": "req-9"}

	first := e.must(http.StatusAccepted, "POST", turns, body)
	s.until(deltaText("Working"))
	// A retry while the run is active is the same turn, not a 409.
	again := e.must(http.StatusAccepted, "POST", turns, body)
	if again["run_id"] != first["run_id"] || again["cursor"] != first["cursor"] {
		t.Fatalf("retry = %v, first = %v", again, first)
	}
	e.wantCode(http.StatusConflict, "request_conflict", "POST", turns, obj{"attachment": a, "text": "something else", "request_id": "req-9"})
	e.runner.Release("hold")
	evs := s.until(terminal)
	head := e.head(conv)
	// And after it finished.
	again = e.must(http.StatusAccepted, "POST", turns, body)
	if again["run_id"] != first["run_id"] {
		t.Fatalf("retry after the run = %v", again)
	}
	if n := len(e.runner.Turns()); n != 1 {
		t.Fatalf("the runner started %d turns for one request_id", n)
	}
	if e.head(conv) != head || count(evs, verb(chatstream.VerbRunStart)) != 0 {
		t.Fatal("a retry published events")
	}
}

func TestRefusalsAndBadRequests(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	c := "/conversations/" + conv

	e.wantCode(http.StatusNotFound, "not_found", "POST", "/agents/nobody/conversation", nil)
	e.wantCode(http.StatusNotFound, "not_found", "GET", "/conversations/conv_nope", nil)
	e.wantCode(http.StatusNotFound, "unknown_attachment", "POST", c+"/turns", obj{"attachment": "att_nope", "text": "hi"})
	e.wantCode(http.StatusNotFound, "unknown_attachment", "DELETE", c+"/attachments/att_nope", nil)
	e.wantCode(http.StatusBadRequest, "invalid_request", "POST", c+"/attachments", obj{"role": "owner", "stream": "deltas"})
	e.wantCode(http.StatusBadRequest, "invalid_request", "POST", c+"/attachments", obj{"role": "viewer"})
	e.wantCode(http.StatusBadRequest, "invalid_request", "POST", c+"/turns", obj{"attachment": a, "text": ""})
	e.wantCode(http.StatusBadRequest, "invalid_request", "POST", c+"/turns", obj{"text": "no attachment"})
	e.wantCode(http.StatusBadRequest, "invalid_request", "POST", c+"/turns", obj{"attachment": a, "text": "x", "meta": []int{1}})
	e.wantCode(http.StatusBadRequest, "invalid_request", "POST", c+"/turns", obj{"attachment": a, "text": "x", "meta": obj{"k": strings.Repeat("m", chat.MaxMetaBytes)}})
	e.wantCode(http.StatusBadRequest, "invalid_request", "POST", c+"/turns", obj{"attachment": a, "text": "x", "context": strings.Repeat("c", chat.MaxContextBytes)})
	e.wantCode(http.StatusBadRequest, "invalid_request", "POST", c+"/lease", nil)
	if n := len(e.runner.Turns()); n != 0 {
		t.Fatalf("%d refused turns reached the runner", n)
	}

	// The client always says where it starts.
	for _, q := range []string{"", "soon", "-1"} {
		if st, status, body := e.open(conv, a, q, ""); st != nil || status != http.StatusBadRequest || body["code"] != "invalid_request" {
			t.Fatalf("stream with after=%q = %d %v", q, status, body)
		}
	}
	if st, status, _ := e.open(conv, a, "", "not-a-cursor"); st != nil || status != http.StatusBadRequest {
		t.Fatalf("stream with a bad Last-Event-ID = %d", status)
	}
	if st, status, body := e.open(conv, "att_nope", "0", ""); st != nil || status != http.StatusNotFound || body["code"] != "unknown_attachment" {
		t.Fatalf("stream with an unknown attachment = %d %v", status, body)
	}

	// Authorize is asked about the owner, with the permission the route needs.
	e.deny(chat.PermRead, true)
	e.wantCode(http.StatusForbidden, "forbidden", "GET", c, nil)
	e.wantCode(http.StatusForbidden, "forbidden", "POST", "/agents/"+agentName+"/conversation", nil)
	e.wantCode(http.StatusForbidden, "forbidden", "POST", c+"/attachments", obj{"role": "viewer", "stream": "deltas"})
	if st, status, _ := e.open(conv, a, "0", ""); st != nil || status != http.StatusForbidden {
		t.Fatalf("stream without read = %d", status)
	}
	e.deny(chat.PermRead, false)
	e.deny(chat.PermControl, true)
	e.must(http.StatusCreated, "POST", c+"/attachments", obj{"role": "viewer", "stream": "deltas"})
	e.wantCode(http.StatusForbidden, "forbidden", "POST", c+"/attachments", obj{"role": "controller", "stream": "deltas"})
	e.wantCode(http.StatusForbidden, "forbidden", "POST", c+"/turns", obj{"attachment": a, "text": "hi"})
	e.wantCode(http.StatusForbidden, "forbidden", "POST", c+"/lease", obj{"attachment": a})
	// Attachment IDs are public on the stream: detaching a controller's takes
	// control, detaching a viewer's does not.
	e.wantCode(http.StatusForbidden, "forbidden", "DELETE", c+"/attachments/"+a, nil)
	viewer := e.must(http.StatusCreated, "POST", c+"/attachments", obj{"role": "viewer", "stream": "committed"})["attachment_id"].(string)
	e.must(http.StatusNoContent, "DELETE", c+"/attachments/"+viewer, nil)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, asked := range e.asked {
		if !strings.HasPrefix(asked, "owner-1/") {
			t.Fatalf("Authorize was asked about %q, want the conversation's owner", asked)
		}
	}
}

func TestGapWhenTheLogNoLongerHoldsTheCursor(t *testing.T) {
	e := newEnv(t)
	conv := e.conversation()
	a := e.attach(conv, chat.RoleController, chat.StreamDeltas)
	s := e.stream(conv, a, "0")
	e.submit(conv, a, "hello")
	all := s.until(terminal)
	head := all[len(all)-1].Seq
	if err := e.log.Trim(context.Background(), conv, streamhub.Retention{MaxRecords: 3}); err != nil {
		t.Fatal(err)
	}
	late := e.resume(conv, a, "2")
	gap, _ := late.next()
	if gap.Verb != chatstream.VerbGap || gap.From != 3 || gap.To != head-3 || gap.Reason != chatstream.GapRetention || gap.Seq != 0 {
		t.Fatalf("first event after a trimmed cursor = %s", mustJSON(t, gap))
	}
	rest := late.until(terminal)
	contiguous(t, rest, head-2) // the stream continues from the oldest retained event
}
