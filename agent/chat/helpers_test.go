package chat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
	"github.com/hollis-labs/libs/agent/chat/chattest"
	"github.com/hollis-labs/libs/agent/chat/memstore"
	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/conformance"
	"github.com/hollis-labs/libs/ui/ui-go/ssekit"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
)

// wait bounds every blocking step of a test. It is a failure timeout, never a
// way to synchronize.
const wait = 20 * time.Second

const agentName = "demo"

// env is one Service behind a real HTTP server, with a manual clock.
type env struct {
	t       *testing.T
	clock   *chattest.Clock
	runner  *chattest.Scripted
	control chat.Control
	log     streamhub.Log
	svc     *chat.Service
	srv     *httptest.Server

	mu     sync.Mutex
	denied map[chat.Permission]bool
	asked  []string // "owner/permission", in order
}

type envOption func(*envSetup)

type envSetup struct {
	cfg        chat.Config
	runnerOpts []chattest.ScriptedOption
}

func withConfig(f func(*chat.Config)) envOption { return func(s *envSetup) { f(&s.cfg) } }

func withRunner(opts ...chattest.ScriptedOption) envOption {
	return func(s *envSetup) { s.runnerOpts = append(s.runnerOpts, opts...) }
}

func newEnv(t *testing.T, opts ...envOption) *env {
	t.Helper()
	e := &env{t: t, clock: chattest.NewClock(), denied: map[chat.Permission]bool{}}
	setup := envSetup{}
	for _, o := range opts {
		o(&setup)
	}
	e.runner = chattest.NewScripted(setup.runnerOpts...)
	cfg := setup.cfg
	if cfg.Control == nil {
		cfg.Control = memstore.NewControl()
	}
	if cfg.Log == nil {
		cfg.Log = streamhub.NewMemoryLog(streamhub.WithMemoryClock(e.clock.Now))
	}
	if cfg.Runner == nil {
		cfg.Runner = e.runner
	}
	if cfg.Agents == nil {
		cfg.Agents = func(_ context.Context, agent string) (chat.Agent, error) {
			if agent != agentName {
				return chat.Agent{}, chat.ErrNotFound
			}
			return chat.Agent{Owner: "owner-1", Scope: "seat-1", Kind: "stable"}, nil
		}
	}
	cfg.Clock, cfg.NewID = e.clock, chattest.IDs()
	e.control, e.log = cfg.Control, cfg.Log
	svc, err := chat.NewService(cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	e.svc = svc
	h, err := chat.NewHandler(svc, func(_ context.Context, owner string, p chat.Permission) error {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.asked = append(e.asked, owner+"/"+string(p))
		if e.denied[p] {
			return errors.New("denied by the test")
		}
		return nil
	}, chat.WithHeartbeat(0))
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	// Mounted under a prefix, as a host would.
	mux := http.NewServeMux()
	mux.Handle("/chat/", http.StripPrefix("/chat", h))
	e.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		e.srv.CloseClientConnections()
		e.srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		if err := svc.Close(ctx); err != nil {
			t.Errorf("Service.Close: %v", err)
		}
	})
	return e
}

func (e *env) deny(p chat.Permission, v bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.denied[p] = v
}

type obj = map[string]any

// call makes one JSON request and returns the status and the decoded body.
func (e *env) call(method, path string, body any) (int, obj) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+"/chat"+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	out := obj{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			e.t.Fatalf("%s %s: body is not JSON: %q", method, path, raw)
		}
	}
	return resp.StatusCode, out
}

// must is call that fails unless the status is want.
func (e *env) must(want int, method, path string, body any) obj {
	e.t.Helper()
	status, out := e.call(method, path, body)
	if status != want {
		e.t.Fatalf("%s %s = %d %v, want %d", method, path, status, out, want)
	}
	return out
}

// wantCode fails unless the call is refused with that status and code.
func (e *env) wantCode(status int, code, method, path string, body any) obj {
	e.t.Helper()
	got, out := e.call(method, path, body)
	if got != status || out["code"] != code {
		e.t.Fatalf("%s %s = %d %v, want %d with code %q", method, path, got, out, status, code)
	}
	return out
}

func (e *env) conversation() string {
	e.t.Helper()
	return e.must(http.StatusOK, "POST", "/agents/"+agentName+"/conversation", nil)["conversation_id"].(string)
}

func (e *env) attach(conv string, role chat.Role, mode chat.StreamMode) string {
	e.t.Helper()
	out := e.must(http.StatusCreated, "POST", "/conversations/"+conv+"/attachments", obj{"role": role, "stream": mode})
	return out["attachment_id"].(string)
}

func (e *env) submit(conv, att, text string) (runID string, cursor uint64) {
	e.t.Helper()
	out := e.must(http.StatusAccepted, "POST", "/conversations/"+conv+"/turns", obj{"attachment": att, "text": text})
	return out["run_id"].(string), uint64(out["cursor"].(float64))
}

func (e *env) get(conv string) obj {
	e.t.Helper()
	return e.must(http.StatusOK, "GET", "/conversations/"+conv, nil)
}

func (e *env) head(conv string) uint64 {
	e.t.Helper()
	return uint64(e.get(conv)["head"].(float64))
}

// sse is one open event stream.
type sse struct {
	t      *testing.T
	cancel context.CancelFunc
	ch     chan chatstream.Event
	// last is the id of the last event read: what a browser would send back
	// as Last-Event-ID.
	last string
}

// open opens an event stream. Exactly one of after and lastEventID is
// normally set. A refusal is returned as status and body with a nil stream.
func (e *env) open(conv, att, after, lastEventID string) (*sse, int, obj) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	url := e.srv.URL + "/chat/conversations/" + conv + "/events?attachment=" + att
	if after != "" {
		url += "&after=" + after
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		e.t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer cancel()
		defer func() { _ = resp.Body.Close() }()
		out := obj{}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return nil, resp.StatusCode, out
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		e.t.Errorf("stream Content-Type = %q", ct)
	}
	s := &sse{t: e.t, cancel: cancel, ch: make(chan chatstream.Event, 4096), last: lastEventID}
	go func() {
		defer close(s.ch)
		defer func() { _ = resp.Body.Close() }()
		prev := ""
		for frame, err := range ssekit.Read(resp.Body) {
			if err != nil {
				return
			}
			var ev chatstream.Event
			if err := json.Unmarshal(frame.Data, &ev); err != nil {
				e.t.Errorf("stream data is not an event: %q", frame.Data)
				return
			}
			if frame.Name != string(ev.Verb) {
				e.t.Errorf("SSE event name %q, verb %q", frame.Name, ev.Verb)
			}
			// The SSE id is the cursor; a gap carries none and leaves the
			// last id in place.
			switch {
			case ev.Seq != 0 && frame.ID != strconv.FormatUint(ev.Seq, 10):
				e.t.Errorf("SSE id %q for seq %d", frame.ID, ev.Seq)
			case ev.Seq == 0 && frame.ID != prev:
				e.t.Errorf("an event without seq changed the SSE id to %q", frame.ID)
			}
			prev = frame.ID
			s.ch <- ev
		}
	}()
	e.t.Cleanup(cancel)
	return s, http.StatusOK, nil
}

// stream is open that must succeed.
func (e *env) stream(conv, att, after string) *sse {
	e.t.Helper()
	s, status, body := e.open(conv, att, after, "")
	if s == nil {
		e.t.Fatalf("open stream = %d %v", status, body)
	}
	return s
}

// resume reconnects the way a browser does: with Last-Event-ID.
func (e *env) resume(conv, att, lastEventID string) *sse {
	e.t.Helper()
	s, status, body := e.open(conv, att, "", lastEventID)
	if s == nil {
		e.t.Fatalf("resume stream = %d %v", status, body)
	}
	return s
}

// next reads one event; ok is false when the server ended the stream.
func (s *sse) next() (chatstream.Event, bool) {
	s.t.Helper()
	select {
	case ev, ok := <-s.ch:
		if ok && ev.Seq != 0 {
			s.last = strconv.FormatUint(ev.Seq, 10)
		}
		return ev, ok
	case <-time.After(wait):
		s.t.Fatal("no event arrived on the stream")
		return chatstream.Event{}, false
	}
}

// until reads events up to and including the first one match accepts.
func (s *sse) until(match func(chatstream.Event) bool) []chatstream.Event {
	s.t.Helper()
	var out []chatstream.Event
	for {
		ev, ok := s.next()
		if !ok {
			s.t.Fatalf("the stream ended before the awaited event; got %s", verbs(out))
		}
		out = append(out, ev)
		if match(ev) {
			return out
		}
	}
}

// rest reads until the server ends the stream.
func (s *sse) rest() []chatstream.Event {
	s.t.Helper()
	var out []chatstream.Event
	for {
		ev, ok := s.next()
		if !ok {
			return out
		}
		out = append(out, ev)
	}
}

// close drops the connection, as a client that goes away does.
func (s *sse) close() { s.cancel() }

// ---- matchers and assertions ----

func terminal(ev chatstream.Event) bool { return ev.IsTerminal() }

func verb(v chatstream.Verb) func(chatstream.Event) bool {
	return func(ev chatstream.Event) bool { return ev.Verb == v }
}

func activityOf(kind string) func(chatstream.Event) bool {
	return func(ev chatstream.Event) bool { return ev.Verb == chatstream.VerbActivity && ev.Kind == kind }
}

func deltaText(text string) func(chatstream.Event) bool {
	return func(ev chatstream.Event) bool { return ev.Verb == chatstream.VerbPartDelta && ev.Text == text }
}

func verbs(evs []chatstream.Event) string {
	parts := make([]string, len(evs))
	for i, ev := range evs {
		parts[i] = string(ev.Verb)
		if ev.Verb == chatstream.VerbActivity {
			parts[i] = ev.Kind
		}
	}
	return strings.Join(parts, " ")
}

func value(t *testing.T, ev chatstream.Event) obj {
	t.Helper()
	out := obj{}
	if err := json.Unmarshal(ev.Value, &out); err != nil {
		t.Fatalf("%s value %q: %v", ev.Kind, ev.Value, err)
	}
	return out
}

func finalText(t *testing.T, ev chatstream.Event) string {
	t.Helper()
	var f struct {
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(ev.Final, &f); err != nil || f.Text == nil {
		t.Fatalf("part.end %q final = %q, want an object with text", ev.PartID, ev.Final)
	}
	return *f.Text
}

// ofRun keeps the events of one run.
func ofRun(evs []chatstream.Event, runID string) []chatstream.Event {
	var out []chatstream.Event
	for _, ev := range evs {
		if ev.RunID == runID {
			out = append(out, ev)
		}
	}
	return out
}

// contiguous fails unless the events' seqs are first, first+1, ... with none
// missing and none repeated.
func contiguous(t *testing.T, evs []chatstream.Event, first uint64) {
	t.Helper()
	for i, ev := range evs {
		if ev.Seq != first+uint64(i) {
			t.Fatalf("event %d (%s) has seq %d, want %d: %s", i, ev.Verb, ev.Seq, first+uint64(i), seqs(evs))
		}
	}
}

// increasing fails unless seqs strictly increase (a filtered stream skips
// some, and never repeats or reorders).
func increasing(t *testing.T, evs []chatstream.Event) {
	t.Helper()
	var last uint64
	for i, ev := range evs {
		if ev.Seq <= last {
			t.Fatalf("event %d (%s) has seq %d after %d: %s", i, ev.Verb, ev.Seq, last, seqs(evs))
		}
		last = ev.Seq
	}
}

func seqs(evs []chatstream.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		fmt.Fprintf(&b, "%d ", ev.Seq)
	}
	return b.String()
}

// wellFormed checks one run's events, as published, against chatstream's own
// validator.
func wellFormed(t *testing.T, run []chatstream.Event) {
	t.Helper()
	conformance.Check(t, run)
}

// message is a message rebuilt the way a committed-only client does it: from
// part.start and part.end alone.
type message struct {
	Role  string
	Parts []string // kind + ":" + the text or JSON of final
}

func committed(t *testing.T, evs []chatstream.Event) []message {
	t.Helper()
	var out []message
	kinds := map[string]string{}
	for _, ev := range evs {
		switch ev.Verb { //nolint:exhaustive // only what a committed-only client reads
		case chatstream.VerbMessageStart:
			out = append(out, message{Role: ev.Role})
		case chatstream.VerbPartStart:
			kinds[ev.PartID] = ev.Kind
		case chatstream.VerbPartEnd:
			if len(out) == 0 {
				t.Fatalf("part.end outside a message")
			}
			text := string(ev.Final)
			if kinds[ev.PartID] != string(chatstream.PartToolCall) {
				text = finalText(t, ev)
			}
			m := &out[len(out)-1]
			m.Parts = append(m.Parts, kinds[ev.PartID]+":"+text)
		}
	}
	return out
}

// streamedText rebuilds each part's text the way a streaming client does it:
// from deltas, keyed by run and part.
func streamedText(evs []chatstream.Event) map[string]string {
	out := map[string]string{}
	for _, ev := range evs {
		if ev.Verb == chatstream.VerbPartDelta {
			out[ev.RunID+"/"+ev.PartID] += ev.Text
		}
	}
	return out
}

func count(evs []chatstream.Event, match func(chatstream.Event) bool) int {
	n := 0
	for _, ev := range evs {
		if match(ev) {
			n++
		}
	}
	return n
}
