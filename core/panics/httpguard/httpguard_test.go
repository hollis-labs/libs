package httpguard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/libs/core/panics"
)

type sink struct {
	mu   sync.Mutex
	recs []panics.Record
}

func (s *sink) Report(r panics.Record) { s.mu.Lock(); s.recs = append(s.recs, r); s.mu.Unlock() }

func setup(t *testing.T, h http.Handler, opts ...Option) (*httptest.Server, *sink) {
	t.Helper()
	s := &sink{}
	g := panics.New(panics.Config{Reporter: s, Redact: panics.Unredacted})
	mux := http.NewServeMux()
	mux.Handle("GET /boom/{id}", h)
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("fine")) })
	ts := httptest.NewServer(Handler(mux, append([]Option{WithGuard(g)}, opts...)...))
	t.Cleanup(ts.Close)
	return ts, s
}

// each request on its own connection, so a dropped connection shows as an error
var client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

func TestPanicBecomesA500WithARefAndTheServerSurvives(t *testing.T) {
	ts, s := setup(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret detail the caller must not see") }))
	resp, err := client.Get(ts.URL + "/boom/tok_abc123")
	if err != nil {
		t.Fatalf("connection dropped instead of a response: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != 500 || body["error"]["code"] != "internal" {
		t.Fatalf("%d %v", resp.StatusCode, body)
	}
	ref, _ := body["error"]["ref"].(string)
	msg, _ := body["error"]["message"].(string)
	if !strings.HasPrefix(ref, "pnc_") || msg != "internal error (ref "+ref+")" || strings.Contains(msg, "secret detail") {
		t.Errorf("%v", body)
	}
	if len(s.recs) != 1 || s.recs[0].Ref != ref {
		t.Fatalf("%+v", s.recs)
	}
	// the route pattern is recorded, never the raw path (it may carry ids or secrets)
	if w := s.recs[0].Where; w != "http GET /boom/{id}" || strings.Contains(w, "tok_abc123") {
		t.Errorf("where = %q", w)
	}
	if r2, err := client.Get(ts.URL + "/ok"); err != nil || r2.StatusCode != 200 {
		t.Errorf("server did not survive: %v", err)
	}
}

func TestAbortHandlerKeepsItsMeaning(t *testing.T) {
	ts, s := setup(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	if _, err := client.Get(ts.URL + "/boom/1"); err == nil {
		t.Error("ErrAbortHandler should abort the connection, as net/http defines")
	}
	if len(s.recs) != 0 {
		t.Errorf("an abort is not a panic report: %+v", s.recs)
	}
}

func TestAStartedResponseIsNotOverwritten(t *testing.T) {
	ts, s := setup(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(202)
		_, _ = w.Write([]byte("partial"))
		panic("after the response started")
	}))
	resp, err := client.Get(ts.URL + "/boom/1")
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != 202 {
			t.Errorf("status rewritten to %d", resp.StatusCode)
		}
	}
	if len(s.recs) != 1 {
		t.Errorf("the panic must still be reported: %+v", s.recs)
	}
}

func TestCustomResponderAndFlusher(t *testing.T) {
	ts, _ := setup(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("x") }),
		WithResponder(func(w http.ResponseWriter, _ *http.Request, pe *panics.Error) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(pe.Error()))
		}))
	resp, err := client.Get(ts.URL + "/boom/1")
	if err != nil || resp.StatusCode != 502 {
		t.Fatalf("%v %v", resp, err)
	}
	rec := httptest.NewRecorder()
	var flushed bool
	Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
			flushed = true
		}
	})).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !flushed {
		t.Error("the wrapper must keep http.Flusher working (streaming responses)")
	}
}
