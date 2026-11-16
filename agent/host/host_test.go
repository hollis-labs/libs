package agenthost

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/conformance"
	"github.com/hollis-labs/libs/ui/ui-go/ssekit"
)

// standIn is a model server for tests: it answers each request with the next scripted
// reply, as the Messages API streams it, and remembers what it was sent.
type standIn struct {
	mu      sync.Mutex
	replies []string
	bodies  []map[string]any
	headers []http.Header
	srv     *httptest.Server
}

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		var t struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(e), &t)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", t.Type, e)
	}
	return b.String()
}

func textReply(text string) string {
	half := len(text) / 2
	return sse(
		`{"type":"message_start","message":{"usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, text[:half]),
		fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, text[half:]),
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	)
}

func toolReply(id, name, inputJSON string) string {
	half := len(inputJSON) / 2
	return sse(
		`{"type":"message_start","message":{"usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Looking."}}`,
		`{"type":"content_block_stop","index":0}`,
		fmt.Sprintf(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}`, id, name),
		fmt.Sprintf(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":%q}}`, inputJSON[:half]),
		fmt.Sprintf(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":%q}}`, inputJSON[half:]),
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	)
}

func newStandIn(t *testing.T, replies ...string) *standIn {
	s := &standIn{replies: replies}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		s.headers = append(s.headers, r.Header.Clone())
		reply := ""
		if len(s.replies) > 0 {
			reply, s.replies = s.replies[0], s.replies[1:]
		}
		s.mu.Unlock()
		switch reply {
		case "":
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"code":"internal","message":"no reply scripted"}`))
		case "hang":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(reply))
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *standIn) sent(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.bodies) {
		return ""
	}
	raw, _ := json.Marshal(s.bodies[i])
	return string(raw)
}

// app is a host mounted the way an application mounts it: owner from a header.
type app struct {
	h   *Host
	srv *httptest.Server
}

func newApp(t *testing.T, s *standIn) *app {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenSQLite(t.Context(), filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := New(Config{Model: "test-model", BaseURL: s.srv.URL, KeyFile: key, System: "You help with a test.", SessionHeader: "X-Session"}, store)
	t.Cleanup(h.Close)
	owner := func(r *http.Request) (string, error) {
		if u := r.Header.Get("X-User"); u != "" {
			return u, nil
		}
		return "", errors.New("no user")
	}
	mux := http.NewServeMux()
	handler := h.Handler("/api/agent", owner)
	mux.Handle("/api/agent", handler)
	mux.Handle("/api/agent/", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &app{h: h, srv: srv}
}

// call makes a request as user u and returns the status and body.
func (a *app) call(t *testing.T, u, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(t.Context(), method, a.srv.URL+"/api/agent"+path, rd)
	if u != "" {
		req.Header.Set("X-User", u)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

func (a *app) turn(t *testing.T, body map[string]any) string {
	t.Helper()
	if body["client_id"] == nil {
		body["client_id"] = "c1"
	}
	code, raw := a.call(t, "u1", "POST", "/turns", body)
	if code != 200 {
		t.Fatalf("turn: %d %s", code, raw)
	}
	var out struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.RunID
}

// events reads a run's stream over HTTP to its end, handing each event to on.
func (a *app) events(t *testing.T, run, lastID string, on func(chatstream.Event)) ([]chatstream.Event, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", a.srv.URL+"/api/agent/runs/"+run+"/events", nil)
	req.Header.Set("X-User", "u1")
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("events: %d", res.StatusCode)
	}
	var out []chatstream.Event
	var ids []string
	for ev, err := range ssekit.Read(res.Body) {
		if err != nil {
			t.Fatalf("stream: %v (after %d events)", err, len(out))
		}
		var e chatstream.Event
		if json.Unmarshal(ev.Data, &e) != nil || e.Verb == "" {
			continue
		}
		out = append(out, e)
		ids = append(ids, ev.ID)
		if on != nil {
			on(e)
		}
		if e.Verb == chatstream.VerbRunFinish || e.Verb == chatstream.VerbRunError || e.Verb == chatstream.VerbRunAbort {
			break
		}
	}
	return out, ids
}

type llmBlock = llmtypes.ContentBlock

func last(evs []chatstream.Event) chatstream.Event { return evs[len(evs)-1] }

func terminals(evs []chatstream.Event) int {
	n := 0
	for _, e := range evs {
		if e.Verb == chatstream.VerbRunFinish || e.Verb == chatstream.VerbRunError || e.Verb == chatstream.VerbRunAbort {
			n++
		}
	}
	return n
}

func textOf(evs []chatstream.Event) string {
	var b strings.Builder
	kinds := map[string]string{}
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartStart {
			kinds[e.PartID] = e.Kind
		}
		if e.Verb == chatstream.VerbPartDelta && kinds[e.PartID] == "text" {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

// clientCall is the ext of a tool call the page is to run, or "" for anything else.
func clientCall(e chatstream.Event) (client, call string) {
	if e.Verb != chatstream.VerbPartEnd || e.Ext == nil {
		return "", ""
	}
	var exec string
	_ = json.Unmarshal(e.Ext[ExtExec], &exec)
	if exec != "client" {
		return "", ""
	}
	_ = json.Unmarshal(e.Ext[ExtClient], &client)
	_ = json.Unmarshal(e.Ext[ExtCall], &call)
	return client, call
}

var (
	lookTool  = map[string]any{"name": "board_read", "description": "Read the board.", "level": "read", "input_schema": map[string]any{"type": "object"}}
	moveTool  = map[string]any{"name": "board_move", "description": "Move a piece.", "input_schema": map[string]any{"type": "object"}} // no level: write
	resetTool = map[string]any{"name": "board_reset", "description": "Start again.", "level": "confirm", "input_schema": map[string]any{"type": "object"}}
)

func TestAPlainAnswerIsStreamedAndKept(t *testing.T) {
	s := newStandIn(t, textReply("Hello there."))
	a := newApp(t, s)
	if code, _ := a.call(t, "", "GET", "", nil); code != 401 {
		t.Fatalf("no owner: %d", code)
	}
	code, raw := a.call(t, "u1", "GET", "", nil)
	if code != 200 || !strings.Contains(string(raw), `"ready":true`) || !strings.Contains(string(raw), `"model":"test-model"`) {
		t.Fatalf("state: %d %s", code, raw)
	}
	run := a.turn(t, map[string]any{"text": "  hi  ", "context": map[string]any{"fen": "8/8/8/8/8/8/8/K6k w"}, "auto": "guide ended"})
	evs, _ := a.events(t, run, "", nil)
	conformance.Check(t, evs)
	if last(evs).Verb != chatstream.VerbRunFinish || last(evs).Reason != "stop" || terminals(evs) != 1 {
		t.Fatalf("ended with %+v", last(evs))
	}
	if got := textOf(evs); got != "Hello there." {
		t.Fatalf("text = %q", got)
	}
	sent := s.sent(0)
	for _, want := range []string{`"model":"test-model"`, `You help with a test.`, `\u003ccontext\u003e`, `8/8/8/8/8/8/8/K6k w`, `"text":"hi"`} {
		if !strings.Contains(sent, want) {
			t.Errorf("request lacks %s:\n%s", want, sent)
		}
	}
	thread, _ := a.h.Store().MainThread(t.Context(), "u1")
	if s.headers[0].Get("Authorization") != "Bearer test-key" || s.headers[0].Get("X-Session") != thread {
		t.Errorf("headers: %v", s.headers[0])
	}
	// The thread has both messages, the person's with its meta.
	_, raw = a.call(t, "u1", "GET", "/thread", nil)
	var th struct {
		ThreadID  string    `json:"thread_id"`
		Messages  []Message `json:"messages"`
		ActiveRun string    `json:"active_run"`
	}
	_ = json.Unmarshal(raw, &th)
	if th.ThreadID != thread || len(th.Messages) != 2 || th.Messages[1].Role != "assistant" || th.Messages[1].Blocks[0].Text != "Hello there." || th.ActiveRun != "" {
		t.Fatalf("thread: %s", raw)
	}
	var m struct {
		Text    string          `json:"text"`
		Context json.RawMessage `json:"context"`
		Auto    string          `json:"auto"`
	}
	_ = json.Unmarshal(th.Messages[0].Meta, &m)
	if m.Text != "hi" || m.Auto != "guide ended" || !strings.Contains(string(m.Context), "K6k") {
		t.Fatalf("meta: %s", th.Messages[0].Meta)
	}
	// Another owner sees neither the thread nor the run.
	_, raw = a.call(t, "u2", "GET", "/thread", nil)
	if strings.Contains(string(raw), "Hello there.") {
		t.Fatal("u2 sees u1's thread")
	}
	if code, _ := a.call(t, "u2", "POST", "/runs/"+run+"/cancel", map[string]any{}); code != 404 {
		t.Fatalf("u2 cancel: %d", code)
	}
	// A new thread starts empty.
	if code, _ := a.call(t, "u1", "DELETE", "/thread", nil); code != 204 {
		t.Fatalf("new thread: %d", code)
	}
	_, raw = a.call(t, "u1", "GET", "/thread", nil)
	_ = json.Unmarshal(raw, &th)
	if th.ThreadID == thread || len(th.Messages) != 0 {
		t.Fatalf("after new thread: %s", raw)
	}
}

func TestAPageToolIsRelayedAndItsResultReachesTheModel(t *testing.T) {
	s := newStandIn(t, toolReply("toolu_1", "board_read", `{"square":"e4"}`), textReply("A knight."))
	a := newApp(t, s)
	run := a.turn(t, map[string]any{"text": "what is on e4?", "tools": []any{lookTool}})
	var asked string
	evs, _ := a.events(t, run, "", func(e chatstream.Event) {
		client, call := clientCall(e)
		if call == "" {
			return
		}
		asked = call
		if client != "c1" || !strings.Contains(string(e.Final), `"square":"e4"`) {
			t.Errorf("call event: client %q final %s", client, e.Final)
		}
		// Still at it, then the answer, with a picture.
		if code, raw := a.call(t, "u1", "POST", "/runs/"+run+"/tool-alive", map[string]any{"call_id": call}); code != 204 {
			t.Errorf("alive: %d %s", code, raw)
		}
		if code, raw := a.call(t, "u1", "POST", "/runs/"+run+"/tool-results", map[string]any{"call_id": call, "content": "white knight",
			"images": []any{map[string]any{"media_type": "image/png", "data": "aGVsbG8="}}}); code != 204 {
			t.Errorf("result: %d %s", code, raw)
		}
	})
	conformance.Check(t, evs)
	if asked != "toolu_1" || last(evs).Verb != chatstream.VerbRunFinish || textOf(evs) != "Looking.A knight." {
		t.Fatalf("asked %q, ended %+v, text %q", asked, last(evs), textOf(evs))
	}
	second := s.sent(1)
	for _, want := range []string{`"tool_use_id":"toolu_1"`, `white knight`, `"media_type":"image/png"`, `"name":"board_read"`} {
		if !strings.Contains(second, want) {
			t.Errorf("second request lacks %s:\n%s", want, second)
		}
	}
	// The answer is in, so a second one is refused.
	if code, _ := a.call(t, "u1", "POST", "/runs/"+run+"/tool-results", map[string]any{"call_id": "toolu_1", "content": "again"}); code != 409 {
		t.Fatalf("second result: %d", code)
	}
}

func TestWriteAsksAndADenyIsToldToTheModel(t *testing.T) {
	s := newStandIn(t,
		toolReply("toolu_1", "board_move", `{"to":"e5"}`), textReply("Fine, I will not."),
		toolReply("toolu_2", "board_move", `{"to":"e6"}`), textReply("Moved."),
		toolReply("toolu_3", "board_move", `{"to":"e7"}`), textReply("Moved again."),
		toolReply("toolu_4", "board_reset", `{"x":1}`), textReply("Reset."),
		toolReply("toolu_5", "board_reset", `{"x":2}`), textReply("Reset again."),
	)
	a := newApp(t, s)
	tools := []any{moveTool, resetTool}
	answer := func(run, decision string) (asks, calls int, evs []chatstream.Event) {
		evs, _ = a.events(t, run, "", func(e chatstream.Event) {
			if e.Verb == chatstream.VerbApprovalRequest {
				asks++
				if code, raw := a.call(t, "u1", "POST", "/runs/"+run+"/approvals", map[string]any{"approval_id": e.ApprovalID, "decision": decision}); code != 204 {
					t.Errorf("approval: %d %s", code, raw)
				}
			}
			if _, call := clientCall(e); call != "" {
				calls++
				a.call(t, "u1", "POST", "/runs/"+run+"/tool-results", map[string]any{"call_id": call, "content": "done"})
			}
		})
		conformance.Check(t, evs)
		return
	}
	// Deny: asked once, not run, and the model is told.
	asks, calls, _ := answer(a.turn(t, map[string]any{"text": "move", "tools": tools}), "deny")
	if asks != 1 || calls != 0 || !strings.Contains(s.sent(1), "declined") || !strings.Contains(s.sent(1), `"is_error":true`) {
		t.Fatalf("deny: asks %d calls %d\n%s", asks, calls, s.sent(1))
	}
	// Always: asked this time, kept, not asked the next.
	if asks, calls, _ = answer(a.turn(t, map[string]any{"text": "move", "tools": tools}), "always"); asks != 1 || calls != 1 {
		t.Fatalf("always: asks %d calls %d", asks, calls)
	}
	if ok, _ := a.h.Store().Granted(t.Context(), "u1", "board_move"); !ok {
		t.Fatal("always was not kept")
	}
	if asks, calls, _ = answer(a.turn(t, map[string]any{"text": "move", "tools": tools}), "deny"); asks != 0 || calls != 1 {
		t.Fatalf("after always: asks %d calls %d", asks, calls)
	}
	// Confirm asks every time, whatever was answered before.
	if asks, calls, _ = answer(a.turn(t, map[string]any{"text": "reset", "tools": tools}), "always"); asks != 1 || calls != 1 {
		t.Fatalf("confirm: asks %d calls %d", asks, calls)
	}
	if asks, _, _ = answer(a.turn(t, map[string]any{"text": "reset", "tools": tools}), "once"); asks != 1 {
		t.Fatalf("confirm again: asks %d", asks)
	}
}

func TestALateToolResultIsRefused(t *testing.T) {
	old := toolQuiet
	toolQuiet = 300 * time.Millisecond
	t.Cleanup(func() { toolQuiet = old })
	s := newStandIn(t, toolReply("toolu_1", "board_read", `{"a":1}`), textReply("It did not answer."))
	a := newApp(t, s)
	run := a.turn(t, map[string]any{"text": "look", "tools": []any{lookTool}})
	var call string
	evs, _ := a.events(t, run, "", func(e chatstream.Event) {
		if _, c := clientCall(e); c != "" {
			call = c // and the page says nothing
		}
	})
	if call == "" || last(evs).Verb != chatstream.VerbRunFinish || !strings.Contains(s.sent(1), "did not answer") {
		t.Fatalf("call %q ended %+v", call, last(evs))
	}
	if code, _ := a.call(t, "u1", "POST", "/runs/"+run+"/tool-results", map[string]any{"call_id": call, "content": "late"}); code != 409 {
		t.Fatalf("late result: %d", code)
	}
	if code, _ := a.call(t, "u1", "POST", "/runs/"+run+"/tool-alive", map[string]any{"call_id": call}); code != 409 {
		t.Fatalf("late alive: %d", code)
	}
}

func TestCancelEndsWithAbort(t *testing.T) {
	s := newStandIn(t, "hang")
	a := newApp(t, s)
	run := a.turn(t, map[string]any{"text": "think hard"})
	// A second message while it runs is refused, and so is a new thread.
	if code, _ := a.call(t, "u1", "POST", "/turns", map[string]any{"text": "and?", "client_id": "c1"}); code != 409 {
		t.Fatalf("busy turn: %d", code)
	}
	if code, _ := a.call(t, "u1", "DELETE", "/thread", nil); code != 409 {
		t.Fatalf("busy new thread: %d", code)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		a.call(t, "u1", "POST", "/runs/"+run+"/cancel", map[string]any{})
	}()
	evs, _ := a.events(t, run, "", nil)
	conformance.Check(t, evs)
	if last(evs).Verb != chatstream.VerbRunAbort || terminals(evs) != 1 {
		t.Fatalf("ended with %+v", last(evs))
	}
	// The thread is free again.
	deadline := time.Now().Add(2 * time.Second)
	for a.h.ActiveRun(mustThread(t, a)) != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.h.ActiveRun(mustThread(t, a)) != "" {
		t.Fatal("the run did not let go of the thread")
	}
}

func mustThread(t *testing.T, a *app) string {
	id, err := a.h.Store().MainThread(t.Context(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestReconnectResumesAfterLastEventID(t *testing.T) {
	s := newStandIn(t, textReply("One two three four."))
	a := newApp(t, s)
	run := a.turn(t, map[string]any{"text": "count"})
	all, ids := a.events(t, run, "", nil)
	if len(all) < 6 {
		t.Fatalf("only %d events", len(all))
	}
	cut := 3
	rest, _ := a.events(t, run, ids[cut-1], nil)
	if len(rest) != len(all)-cut {
		t.Fatalf("resumed with %d events, want %d", len(rest), len(all)-cut)
	}
	for i, e := range rest {
		if e.Seq != all[cut+i].Seq || e.Verb != all[cut+i].Verb {
			t.Fatalf("event %d: %+v, want %+v", i, e, all[cut+i])
		}
	}
	if last(rest).Verb != chatstream.VerbRunFinish {
		t.Fatalf("resumed stream ended with %+v", last(rest))
	}
}

func TestAnUnansweredCallIsRepairedInHistory(t *testing.T) {
	// A thread whose last run died between a call and its result still sends.
	s := newStandIn(t, textReply("Hi again."))
	a := newApp(t, s)
	thread := mustThread(t, a)
	input := map[string]any{}
	_, _ = a.h.Store().Append(t.Context(), thread, "run_x", "user", []llmBlock{{Type: "text", Text: "go"}}, json.RawMessage(`{"text":"go"}`))
	_, _ = a.h.Store().Append(t.Context(), thread, "run_x", "assistant", []llmBlock{{Type: "tool_use", ID: "toolu_dead", Name: "board_read", Input: &input}}, nil)
	run := a.turn(t, map[string]any{"text": "hello"})
	evs, _ := a.events(t, run, "", nil)
	if last(evs).Verb != chatstream.VerbRunFinish || !strings.Contains(s.sent(0), `"tool_use_id":"toolu_dead"`) {
		t.Fatalf("ended %+v\n%s", last(evs), s.sent(0))
	}
}

func TestCompaction(t *testing.T) {
	s := newStandIn(t, textReply("one"), textReply("two"), textReply("SUMMARY: they said one and two."), textReply("three"))
	a := newApp(t, s)
	for _, text := range []string{"first", "second", "third"} {
		if text == "third" {
			a.h.cfg.CompactAt = 15 // each stand-in answer reports 10 in + 5 out
		}
		run := a.turn(t, map[string]any{"text": text})
		evs, _ := a.events(t, run, "", nil)
		if last(evs).Verb != chatstream.VerbRunFinish {
			t.Fatalf("%s: %+v", text, last(evs))
		}
	}
	// The third run summarised the first two turns, then sent the summary and the rest.
	if !strings.Contains(s.sent(2), "first") || !strings.Contains(s.sent(3), "SUMMARY: they said one and two.") || strings.Contains(s.sent(3), `"text":"first"`) {
		t.Fatalf("summary request:\n%s\nnext:\n%s", s.sent(2), s.sent(3))
	}
	if sum, upto, _ := a.h.Store().Summary(t.Context(), mustThread(t, a)); sum == "" || upto == 0 {
		t.Fatalf("summary not kept: %q %d", sum, upto)
	}
}

func TestSQLiteInTheApplicationsDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE games (id TEXT); PRAGMA user_version = 7`); err != nil {
		t.Fatal(err)
	}
	for range 2 { // opening twice upgrades once
		s, err := NewSQLite(t.Context(), db)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.MainThread(t.Context(), "u1"); err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
	}
	var v, n int
	_ = db.QueryRow(`PRAGMA user_version`).Scan(&v)
	_ = db.QueryRow(`SELECT COUNT(*) FROM agenthost_threads`).Scan(&n)
	if v != 7 || n != 1 {
		t.Fatalf("the application's user_version is %d, threads %d", v, n)
	}
}
