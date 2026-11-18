package chat_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	chat "github.com/hollis-labs/libs/agent/chat"
	"github.com/hollis-labs/libs/agent/chat/chattest"
	"github.com/hollis-labs/libs/agent/chat/sqlitestore"
	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
)

// e2eScript is one answer with a tool call, with two points where the run
// waits so the clients can drop and come back in the middle of a message.
func e2eScript(chat.Turn) []chattest.Step {
	delta := func(s string) chattest.Step {
		return chattest.Step{Event: chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: "p1", Text: s}}
	}
	return chattest.Join(
		chattest.Start("scripted", "e2e"),
		[]chattest.Step{
			{Event: chatstream.Event{Verb: chatstream.VerbStepStart, StepID: "s1"}},
			{Event: chatstream.Event{Verb: chatstream.VerbMessageStart, MessageID: "m1", Role: "assistant"}},
			{Event: chatstream.Event{Verb: chatstream.VerbPartStart, PartID: "p1", Kind: "text"}},
			delta("The "), delta("quick "),
		},
		chattest.Gate("one"),
		[]chattest.Step{delta("brown "), delta("fox ")},
		chattest.Gate("two"),
		[]chattest.Step{
			delta("jumps."),
			{Event: chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: "p1"}}, // final left to the Service
		},
		chattest.ToolCall("c1", "lookup", map[string]int{"n": 1}),
		[]chattest.Step{
			{Event: chatstream.Event{Verb: chatstream.VerbMessageEnd, MessageID: "m1"}},
			{Event: chatstream.Event{Verb: chatstream.VerbStepFinish, StepID: "s1"}},
		},
		chattest.Message("m2", chattest.ToolResult("r1", "c1", "one dog", false), chattest.Text("p2", "Over the ", "lazy dog.")),
		chattest.Finish(chatstream.FinishStop),
	)
}

// TestEndToEndTwoClientsReconnecting drives the real handler over HTTP with a
// controller that streams deltas and a committed-only viewer. Both drop and
// reconnect twice, once in the middle of a message.
func TestEndToEndTwoClientsReconnecting(t *testing.T) {
	backends := map[string]func(t *testing.T, c *chat.Config){
		"memory": func(*testing.T, *chat.Config) {},
		"sqlite": func(t *testing.T, c *chat.Config) {
			st, err := sqlitestore.Open(context.Background(), filepath.Join(t.TempDir(), "chat.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			c.Log, c.Control = st, st
		},
	}
	for name, backend := range backends {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, withRunner(chattest.WithScript(e2eScript)), withConfig(func(c *chat.Config) { backend(t, c) }))
			conv := e.conversation()
			ctl := e.attach(conv, chat.RoleController, chat.StreamDeltas)
			view := e.attach(conv, chat.RoleViewer, chat.StreamCommitted)

			// First paint is the same request as a reconnect: after=0.
			cs := e.stream(conv, ctl, "0")
			vs := e.stream(conv, view, "0")
			out := e.must(http.StatusAccepted, "POST", "/conversations/"+conv+"/turns",
				obj{"attachment": ctl, "text": "tell me", "request_id": "r-1", "meta": obj{"auto": false}})
			runID := out["run_id"].(string)

			// Both read into the middle of the first message, then drop.
			cGot := cs.until(deltaText("quick "))
			vGot := vs.until(func(ev chatstream.Event) bool { return ev.Verb == chatstream.VerbPartStart && ev.PartID == "p1" })
			cCursor, vCursor := cs.last, vs.last
			cs.close()
			vs.close()
			e.runner.Release("one")

			// Both reconnect with Last-Event-ID while the run is in progress.
			cs = e.resume(conv, ctl, cCursor)
			vs = e.resume(conv, view, vCursor)
			cGot = append(cGot, cs.until(deltaText("fox "))...)
			live := len(cGot)
			cCursor = cs.last
			cs.close() // the controller drops again; the viewer stays
			e.runner.Release("two")
			vGot = append(vGot, vs.until(terminal)...)

			// The controller comes back after the run has finished.
			cs = e.resume(conv, ctl, cCursor)
			cGot = append(cGot, cs.until(terminal)...)

			// No loss and no duplicates, for either client.
			contiguous(t, cGot[:live], 1) // while live the deltas client misses nothing at all
			increasing(t, cGot)
			increasing(t, vGot)
			if n := count(vGot, verb(chatstream.VerbPartDelta)); n != 0 {
				t.Fatalf("the committed-only viewer was sent %d part.delta", n)
			}
			if cGot[len(cGot)-1].Seq != vGot[len(vGot)-1].Seq || cGot[len(cGot)-1].Verb != chatstream.VerbRunFinish {
				t.Fatalf("clients ended at seq %d (%s) and %d", cGot[len(cGot)-1].Seq, cGot[len(cGot)-1].Verb, vGot[len(vGot)-1].Seq)
			}
			// The viewer saw every event but the deltas: the same seqs as the
			// controller, minus those.
			cSeqs := map[uint64]chatstream.Verb{}
			for _, ev := range cGot {
				cSeqs[ev.Seq] = ev.Verb
			}
			for _, ev := range vGot {
				if cSeqs[ev.Seq] != ev.Verb {
					t.Fatalf("viewer event seq %d (%s) is not the controller's (%s)", ev.Seq, ev.Verb, cSeqs[ev.Seq])
				}
			}

			// The deltas the controller received live, in order, once each.
			if got := streamedText(cGot)[runID+"/p1"]; got != "The quick brown fox " {
				t.Fatalf("controller's deltas = %q", got)
			}
			// Both arrive at the same messages; the viewer from part.start and
			// part.end alone, with final filled by the Service for the part the
			// runner streamed.
			want := [][]string{
				{"text:tell me"},
				{"text:The quick brown fox jumps.", `tool_call:{"n":1}`},
				{"tool_result:one dog", "text:Over the lazy dog."},
			}
			for who, evs := range map[string][]chatstream.Event{"controller": cGot, "viewer": vGot} {
				run := ofRun(evs, runID)
				if got := partsOf(committed(t, run)); string(mustJSON(t, got)) != string(mustJSON(t, want)) {
					t.Fatalf("%s rebuilt %s", who, mustJSON(t, got))
				}
				if run[0].Verb != chatstream.VerbRunStart || run[1].MetaString("request_id") != "r-1" {
					t.Fatalf("%s: run begins %s", who, verbs(run[:2]))
				}
			}
			wellFormed(t, ofRun(vGot, runID))

			// A fresh client painting from 0 gets the same thing in one pass
			// over committed events.
			fresh := e.attach(conv, chat.RoleViewer, chat.StreamDeltas)
			hist := e.stream(conv, fresh, "0").until(terminal)
			if count(hist, verb(chatstream.VerbPartDelta)) != 0 {
				t.Fatal("history was sent with deltas")
			}
			if got := partsOf(committed(t, ofRun(hist, runID))); string(mustJSON(t, got)) != string(mustJSON(t, want)) {
				t.Fatalf("history rebuilt %s", mustJSON(t, got))
			}
			// The viewer never could type; the controller still can.
			e.wantCode(http.StatusForbidden, "lease_required", "POST", "/conversations/"+conv+"/turns", obj{"attachment": view, "text": "me too"})
			if v := e.get(conv); v["lease"].(obj)["attachment"] != ctl || v["active_run"] != nil {
				t.Fatalf("after the run: %v", v)
			}
		})
	}
}
