package workclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func staticToken(tok string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return tok, nil }
}

// fake records requests and answers from a table keyed by "METHOD path".
type fake struct {
	mu      sync.Mutex
	reqs    []*http.Request
	bodies  []string
	answers map[string]func(w http.ResponseWriter, r *http.Request)
}

func newFake(t *testing.T) (*fake, *httptest.Server) {
	t.Helper()
	f := &fake{answers: map[string]func(http.ResponseWriter, *http.Request){}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, r)
		f.bodies = append(f.bodies, string(b))
		h := f.answers[r.Method+" "+r.URL.EscapedPath()]
		f.mu.Unlock()
		if h == nil {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":"not_found","message":"There is no such thing."}`))
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fake) on(key string, status int, body string) {
	f.answers[key] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func (f *fake) last() (*http.Request, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1], f.bodies[len(f.bodies)-1]
}

func newClient(t *testing.T, url string, mod ...func(*Config)) *Client {
	t.Helper()
	c := Config{URL: url, Token: staticToken("k1"), OwnerNS: "team-a", Principal: "msg://user/a", CorrelationPrefix: "test"}
	for _, m := range mod {
		m(&c)
	}
	cl, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

func TestNewRefusesBadConfig(t *testing.T) {
	for _, c := range []Config{
		{URL: "", Token: staticToken("k")},
		{URL: "ftp://x", Token: staticToken("k")},
		{URL: "http://", Token: staticToken("k")},
		{URL: "http://x"},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("New(%+v) = nil error", c)
		}
	}
}

func TestHeadersAndContextOverrides(t *testing.T) {
	f, srv := newFake(t)
	f.on("GET /v1/work", 200, `{"workspaces":null,"presets":[]}`)
	cl := newClient(t, srv.URL+"/")

	l, err := cl.Workspaces(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if l.Workspaces == nil {
		t.Error("Workspaces = nil, want an empty list")
	}
	r, _ := f.last()
	if got := r.Header.Get("Authorization"); got != "Bearer k1" {
		t.Errorf("Authorization = %q", got)
	}
	if r.Header.Get(HeaderOwnerNS) != "team-a" || r.Header.Get(HeaderPrincipal) != "msg://user/a" {
		t.Errorf("identity headers = %q, %q", r.Header.Get(HeaderOwnerNS), r.Header.Get(HeaderPrincipal))
	}
	if r.Header.Get(HeaderActor) != "" {
		t.Errorf("X-Actor sent without being set: %q", r.Header.Get(HeaderActor))
	}
	if !strings.HasPrefix(r.Header.Get(HeaderCorrelation), "test-") {
		t.Errorf("correlation id = %q", r.Header.Get(HeaderCorrelation))
	}

	ctx := WithActor(WithoutPrincipal(WithOwnerNS(context.Background(), "team-b")), "agent:x")
	ctx = WithCorrelation(ctx, "req-7")
	if _, err := cl.Workspaces(ctx, "planner"); err != nil {
		t.Fatal(err)
	}
	r, _ = f.last()
	if r.Header.Get(HeaderOwnerNS) != "team-b" || r.Header.Get(HeaderActor) != "agent:x" {
		t.Errorf("overrides not sent: ns %q actor %q", r.Header.Get(HeaderOwnerNS), r.Header.Get(HeaderActor))
	}
	if _, set := r.Header[HeaderPrincipal]; set {
		t.Error("WithoutPrincipal still sent X-Principal-URN")
	}
	if r.Header.Get(HeaderCorrelation) != "req-7" {
		t.Errorf("correlation = %q, want the caller's", r.Header.Get(HeaderCorrelation))
	}
	// An empty override keeps the configured value rather than dropping the header.
	if _, err := cl.Workspaces(WithPrincipal(WithOwnerNS(context.Background(), ""), ""), "planner"); err != nil {
		t.Fatal(err)
	}
	r, _ = f.last()
	if r.Header.Get(HeaderOwnerNS) != "team-a" || r.Header.Get(HeaderPrincipal) != "msg://user/a" {
		t.Errorf("empty overrides: ns %q principal %q", r.Header.Get(HeaderOwnerNS), r.Header.Get(HeaderPrincipal))
	}
	if r.URL.Query().Get("app") != "planner" {
		t.Errorf("app = %q", r.URL.Query().Get("app"))
	}
	if cl.OwnerNS(ctx) != "team-b" || cl.OwnerNS(context.Background()) != "team-a" {
		t.Error("OwnerNS does not follow the context")
	}
}

func TestPathsAreEscaped(t *testing.T) {
	f, srv := newFake(t)
	f.on("GET /v1/work/a%2Fb/items/x%20y", 200, `{"id":"x y","kind":"task","notes":[]}`)
	cl := newClient(t, srv.URL)
	d, err := cl.Detail(context.Background(), "a/b", "x y")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "x y" || d.Kind != "task" {
		t.Errorf("detail = %+v", d.Card)
	}
}

func TestBaseURLWithPath(t *testing.T) {
	f, srv := newFake(t)
	f.on("POST /svc/v1/work/w/items", 201, `{"id":"01X"}`)
	cl := newClient(t, srv.URL+"/svc")
	id, err := cl.MakeItem(context.Background(), "w", NewItem{Kind: "task", Title: "T"})
	if err != nil || id != "01X" {
		t.Fatalf("MakeItem = %q, %v", id, err)
	}
	r, body := f.last()
	if r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
	}
	var got NewItem
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Title != "T" {
		t.Errorf("body = %s", body)
	}
}

func TestErrors(t *testing.T) {
	f, srv := newFake(t)
	f.on("PUT /v1/work/w/items/1", 409, `{"code":"conflict","message":"stale","conflict":true,"rev":7,"suggestions":2}`)
	f.on("POST /v1/work/w/items/1/signoff", 409, `{"code":"conflict","message":"open notes","open":3,"round":2}`)
	f.on("POST /v1/work/w/items/1/torque", 502, `{"code":"upstream_error","message":"comment failed","task":"T-1"}`)
	f.on("POST /v1/work/w/items/1/round", 403, `{"code":"forbidden","message":"owners only"}`)
	f.on("PUT /v1/work/w/owners", 401, `{"code":"unauthenticated","message":"no key"}`)
	f.on("POST /v1/work/w/items/1/cut", 503, `{"code":"unavailable","message":"no caster"}`)
	f.on("POST /v1/work/w/items/1/restore", 500, `boom`)
	cl := newClient(t, srv.URL)
	ctx := context.Background()

	_, err := cl.EditItem(ctx, "w", "1", Edit{BaseRev: 3})
	var e *Error
	if !errors.As(err, &e) || e.Conflict == nil || e.Conflict.Rev != 7 || e.Conflict.Suggestions != 2 || e.Status != 409 || e.Code != "conflict" {
		t.Errorf("stale edit: %#v", err)
	}

	_, err = cl.SignOff(ctx, "w", "1", false)
	if !errors.As(err, &e) || e.OpenNotes == nil || e.OpenNotes.Open != 3 || e.OpenNotes.Round != 2 || e.Conflict != nil {
		t.Errorf("open notes: %#v", err)
	}

	task, created, err := cl.SendToTorque(ctx, "w", "1")
	if task != "T-1" || created || err == nil {
		t.Errorf("SendToTorque = %q, %v, %v", task, created, err)
	}

	if _, err := cl.SendRound(ctx, "w", "1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("403 = %v", err)
	}
	if err := cl.SetOwners(ctx, "w", nil); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("401 = %v", err)
	}
	if _, body := f.last(); body != `{"owners":[]}` {
		t.Errorf("SetOwners(nil) body = %s", body)
	}
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("502 = %v", err)
	}
	if _, err := cl.EditItem(ctx, "w", "1", Edit{}); !errors.Is(err, ErrConflict) {
		t.Errorf("409 = %v", err)
	}
	if err := cl.Cut(ctx, "w", "1"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("503 = %v", err)
	}
	err = cl.Restore(ctx, "w", "1")
	if !errors.As(err, &e) || e.Status != 500 || e.Message != "boom" || e.Body != nil {
		t.Errorf("500 = %#v", err)
	}
	if _, err := cl.Open(ctx, "missing"); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
		t.Errorf("404 = %v", err)
	}
}

func TestUnreachableAndBadAnswer(t *testing.T) {
	f, srv := newFake(t)
	f.on("GET /v1/work/w", 200, `{"space":`)
	cl := newClient(t, srv.URL)
	if _, err := cl.Open(context.Background(), "w"); !errors.Is(err, ErrBadAnswer) {
		t.Errorf("truncated answer = %v", err)
	}
	srv.Close()
	if _, err := cl.Open(context.Background(), "w"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("closed server = %v", err)
	}
}

func TestTokenErrorStopsTheRequest(t *testing.T) {
	f, srv := newFake(t)
	f.on("GET /v1/work", 200, `{}`)
	cl := newClient(t, srv.URL, func(c *Config) {
		c.Token = func(context.Context) (string, error) { return "", errors.New("no key") }
	})
	if _, err := cl.Workspaces(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "no key") {
		t.Errorf("err = %v", err)
	}
	if len(f.reqs) != 0 {
		t.Error("a request was sent without a key")
	}
}

func TestTokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte("  secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := TokenFile(path)(context.Background())
	if err != nil || tok != "secret-value" {
		t.Errorf("TokenFile = %q, %v", tok, err)
	}
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := TokenFile(path)(context.Background()); err == nil {
		t.Error("an empty key file was accepted")
	}
	if _, err := TokenFile(filepath.Join(dir, "none"))(context.Background()); err == nil {
		t.Error("a missing key file was accepted")
	}
}

func TestRoutesAndBodies(t *testing.T) {
	f, srv := newFake(t)
	cl := newClient(t, srv.URL)
	ctx := context.Background()
	title := "New"
	archived := true
	text := "t"

	type call struct {
		key  string
		ans  string
		run  func() error
		body string // "" = not checked
	}
	calls := []call{
		{"POST /v1/work", `{"workspace":"w","problem":"half"}`, func() error {
			c, err := cl.NewWorkspace(ctx, NewWorkspace{Workspace: "w", Template: "t1"})
			if err == nil && (c.Workspace != "w" || c.Problem != "half") {
				return errors.New("created = " + c.Workspace + "/" + c.Problem)
			}
			return err
		}, `{"workspace":"w","template":"t1"}`},
		{"PUT /v1/work/w", `{"updated":true}`, func() error {
			return cl.UpdateWorkspace(ctx, "w", WorkspaceUpdate{Title: &title, Archived: &archived})
		}, `{"title":"New","archived":true}`},
		{"DELETE /v1/work/w", `{"deleted":true}`, func() error { return cl.DeleteWorkspace(ctx, "w") }, ""},
		{"GET /v1/work/w/log", `{"revisions":[{"rev":2,"at":"x","title":"T"}]}`, func() error {
			r, err := cl.Log(ctx, "w", "i.1")
			if err == nil && (len(r) != 1 || r[0].Rev != 2) {
				return errors.New("bad log")
			}
			return err
		}, ""},
		{"GET /v1/work/w/at", `{"name":"i.1","rev":2,"content":"c","title":"T","sections":{"Body":"b"}}`, func() error {
			v, err := cl.At(ctx, "w", "i.1", 2)
			if err == nil && (v.Content != "c" || v.Sections["Body"] != "b") {
				return errors.New("bad version")
			}
			return err
		}, ""},
		{"PUT /v1/work/w/items/1/move", `{"rev":4}`, func() error {
			rev, err := cl.Move(ctx, "w", "1", "stage", "done")
			if err == nil && rev != 4 {
				return errors.New("bad rev")
			}
			return err
		}, `{"key":"stage","value":"done"}`},
		{"PUT /v1/work/w/items/1/place", `{"ok":true}`, func() error {
			return cl.Place(ctx, "w", "1", Placement{Parent: "p", View: "v"})
		}, `{"parent":"p","before":"","view":"v"}`},
		{"GET /v1/work/w/items/1/torque", `{"task":null}`, func() error {
			it, err := cl.TorqueStatus(ctx, "w", "1")
			if err == nil && it.Task != nil {
				return errors.New("task should be nil")
			}
			return err
		}, ""},
		{"POST /v1/work/w/items/1/notes", `{"id":"n1","kind":"comment","rev":1}`, func() error {
			n, err := cl.AddNote(ctx, "w", "1", NewNote{Kind: "comment", Text: "hi"})
			if err == nil && n.ID != "n1" {
				return errors.New("bad note")
			}
			return err
		}, ""},
		{"PUT /v1/work/w/notes/n1", `{"ok":true}`, func() error { return cl.EditNote(ctx, "w", "n1", &text, nil) }, `{"text":"t"}`},
		{"DELETE /v1/work/w/notes/n1", `{"ok":true}`, func() error { return cl.DropNote(ctx, "w", "n1") }, ""},
		{"DELETE /v1/work/w/items/1/signoff", `{"ok":true}`, func() error { return cl.Unsign(ctx, "w", "1") }, ""},
		{"POST /v1/work/w/views", `{"id":"v9","rev":1}`, func() error {
			id, rev, err := cl.NewView(ctx, "w", nil)
			if err == nil && (id != "v9" || rev != 1) {
				return errors.New("bad view")
			}
			return err
		}, `{}`},
		{"PUT /v1/work/w/views/v9", `{"id":"v9","rev":2}`, func() error {
			_, err := cl.SaveView(ctx, "w", "v9", map[string]any{"title": "B"})
			return err
		}, `{"title":"B"}`},
		{"PUT /v1/work/w/views/v9/layout", `{"rev":3}`, func() error {
			_, err := cl.PatchLayout(ctx, "w", "v9", LayoutPatch{Cards: map[string]*Pos{"1": {X: 1, Y: 2}, "2": nil}})
			return err
		}, `{"cards":{"1":{"x":1,"y":2},"2":null},"stacks":null,"notes":null}`},
	}
	for _, c := range calls {
		f.on(c.key, 200, c.ans)
		if err := c.run(); err != nil {
			t.Errorf("%s: %v", c.key, err)
			continue
		}
		r, body := f.last()
		if got := r.Method + " " + r.URL.EscapedPath(); got != c.key {
			t.Errorf("sent %s, want %s", got, c.key)
		}
		if c.body != "" && strings.TrimSpace(body) != c.body {
			t.Errorf("%s body = %s, want %s", c.key, body, c.body)
		}
	}
}

func TestEventStreamFromHead(t *testing.T) {
	f, srv := newFake(t)
	f.on("GET /healthz", 200, `{"status":"ok","events_head":41}`)
	f.answers["GET /v1/events"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("since") != "41" || r.Header.Get("Accept") != "text/event-stream" {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": keep-alive\n\n"+
			"id: 42\nevent: work.item.changed\ndata: {\"seq\":42,\"kind\":\"work.item.changed\",\"workspace\":\"w\",\"name\":\"i.1\",\"rev\":3}\n\n"+
			"id: 43\nevent: work.note.changed\ndata: {\"seq\":43,\"kind\":\"work.note.changed\",\"note\":\"n1\"}\n\n")
	}
	cl := newClient(t, srv.URL)
	stream, from, err := cl.EventStream(context.Background(), FromHead)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if from != 41 {
		t.Errorf("from = %d", from)
	}
	er := NewEventReader(stream)
	ev, err := er.Next()
	if err != nil || ev.Seq != 42 || ev.Name != "i.1" || ev.Rev != 3 {
		t.Fatalf("first = %+v, %v", ev, err)
	}
	ev, err = er.Next()
	if err != nil || ev.Seq != 43 || ev.Note != "n1" {
		t.Fatalf("second = %+v, %v", ev, err)
	}
	if _, err := er.Next(); err != io.EOF {
		t.Errorf("end = %v", err)
	}
}

func TestEventStreamRefused(t *testing.T) {
	f, srv := newFake(t)
	f.on("GET /v1/events", 403, `{"code":"forbidden","message":"admin only"}`)
	cl := newClient(t, srv.URL)
	if _, _, err := cl.EventStream(context.Background(), 5); !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v", err)
	}
}

func TestHeadMissing(t *testing.T) {
	f, srv := newFake(t)
	f.on("GET /healthz", 200, `{"status":"ok"}`)
	cl := newClient(t, srv.URL)
	if _, err := cl.Head(context.Background()); !errors.Is(err, ErrBadAnswer) {
		t.Errorf("err = %v", err)
	}
}

func TestEventReaderBadData(t *testing.T) {
	er := NewEventReader(strings.NewReader("data: {nope\n\n"))
	if _, err := er.Next(); !errors.Is(err, ErrBadAnswer) {
		t.Errorf("err = %v", err)
	}
}

func TestDotIDsAreRefused(t *testing.T) {
	f, srv := newFake(t)
	cl := newClient(t, srv.URL)
	ctx := context.Background()
	for _, id := range []string{"", ".", ".."} {
		if err := cl.DropNote(ctx, "w", id); !errors.Is(err, ErrBadID) {
			t.Errorf("DropNote(%q) = %v", id, err)
		}
		if _, err := cl.Open(ctx, id); !errors.Is(err, ErrBadID) {
			t.Errorf("Open(%q) = %v", id, err)
		}
	}
	if len(f.reqs) != 0 {
		t.Errorf("%d requests sent for refused ids", len(f.reqs))
	}
	// Dots inside an id are fine.
	f.on("GET /v1/work/w/items/a..b", 200, `{"id":"a..b"}`)
	if d, err := cl.Detail(ctx, "w", "a..b"); err != nil || d.ID != "a..b" {
		t.Errorf("Detail(a..b) = %+v, %v", d.Card, err)
	}
}

func TestRedirectedWritesAreNotFollowed(t *testing.T) {
	f, srv := newFake(t)
	f.answers["DELETE /v1/work/w/notes/n1"] = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v1/work/w", http.StatusTemporaryRedirect)
	}
	f.on("DELETE /v1/work/w", 200, `{"deleted":true}`)
	f.answers["GET /v1/work/old"] = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v1/work/new", http.StatusTemporaryRedirect)
	}
	f.on("GET /v1/work/new", 200, `{"space":{"default_view":"v1"}}`)
	cl := newClient(t, srv.URL, func(c *Config) { c.HTTP = &http.Client{} })
	if err := cl.DropNote(context.Background(), "w", "n1"); err == nil {
		t.Error("a redirected delete was followed")
	}
	for _, r := range f.reqs {
		if r.URL.Path == "/v1/work/w" {
			t.Fatal("the redirect reached the workspace delete")
		}
	}
	if o, err := cl.Open(context.Background(), "old"); err != nil || o.Space.Default != "v1" {
		t.Errorf("a redirected read: %+v, %v", o, err)
	}
}

func TestEditClearsWithAnEmptyList(t *testing.T) {
	f, srv := newFake(t)
	f.on("PUT /v1/work/w/items/1", 200, `{"rev":2}`)
	cl := newClient(t, srv.URL)
	var tags []string
	if _, err := cl.EditItem(context.Background(), "w", "1", Edit{BaseRev: 1, Tags: &tags}); err != nil {
		t.Fatal(err)
	}
	_, body := f.last()
	var got map[string]any
	_ = json.Unmarshal([]byte(body), &got)
	if l, ok := got["tags"].([]any); !ok || len(l) != 0 {
		t.Errorf("tags = %#v, want []", got["tags"])
	}
	if got["links"] != nil {
		t.Errorf("links = %#v, want null (left as it is)", got["links"])
	}
}

func TestHeadFromADegradedService(t *testing.T) {
	f, srv := newFake(t)
	f.on("GET /healthz", 503, `{"status":"degraded","events_head":9}`)
	cl := newClient(t, srv.URL)
	if h, err := cl.Head(context.Background()); err != nil || h != 9 {
		t.Errorf("Head = %d, %v", h, err)
	}
}

func TestStreamThatIsNotEvents(t *testing.T) {
	f, srv := newFake(t)
	f.answers["GET /v1/events"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>")
	}
	cl := newClient(t, srv.URL)
	if _, _, err := cl.EventStream(context.Background(), 0); !errors.Is(err, ErrBadAnswer) {
		t.Errorf("err = %v", err)
	}
}

func TestLongTextAnswerIsCapped(t *testing.T) {
	f, srv := newFake(t)
	f.on("POST /v1/work/w/items/1/cut", 500, strings.Repeat("x", 100<<10))
	cl := newClient(t, srv.URL)
	var e *Error
	if err := cl.Cut(context.Background(), "w", "1"); !errors.As(err, &e) || len(e.Message) > maxMessage+8 {
		t.Errorf("message length %d", len(e.Message))
	}
}

func TestOversizeEventIsBadAnswer(t *testing.T) {
	er := NewEventReader(strings.NewReader("data: " + strings.Repeat("x", 2<<20) + "\n\n"))
	if _, err := er.Next(); !errors.Is(err, ErrBadAnswer) {
		t.Errorf("err = %v", err)
	}
}

func TestCappedMessageKeepsWholeCharacters(t *testing.T) {
	e := answerError(500, []byte(strings.Repeat("é", maxMessage)))
	if !utf8.ValidString(e.Message) {
		t.Error("the cut split a character")
	}
}
