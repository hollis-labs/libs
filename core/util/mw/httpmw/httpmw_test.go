package httpmw_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/core/util/mw"
	"github.com/hollis-labs/libs/core/util/mw/httpmw"
	"github.com/hollis-labs/libs/core/util/svcerr"
)

func quiet() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

func TestStandardChain(t *testing.T) {
	log, buf := quiet()
	s, err := httpmw.Assemble(mw.Options{},
		httpmw.Recover(log), httpmw.RequestID(), httpmw.AccessLog(log), httpmw.BodyLimit(8),
	)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Then(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/panic" {
			panic("boom")
		}
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "too big", http.StatusRequestEntityTooLarge)
			return
		}
		io.WriteString(w, "ok")
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/x", strings.NewReader("hi")))
	if rr.Code != 200 || rr.Header().Get("X-Request-ID") == "" {
		t.Fatalf("%d %v", rr.Code, rr.Header())
	}
	id := rr.Header().Get("X-Request-ID")
	if !strings.Contains(buf.String(), "request_id="+id) {
		t.Fatalf("log missing id: %s", buf)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/x", strings.NewReader("way too long")))
	if rr.Code != 413 {
		t.Fatalf("body limit: %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-Request-ID", "bad id with spaces")
	h.ServeHTTP(rr, req)
	if got := rr.Header().Get("X-Request-ID"); got == "bad id with spaces" || got == "" {
		t.Fatalf("hostile id reused: %q", got)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/panic", nil))
	if rr.Code != 500 || strings.Contains(rr.Body.String(), "boom") {
		t.Fatalf("panic: %d %s", rr.Code, rr.Body)
	}
}

func TestAssemblyRejectsAccessLogWithoutRequestID(t *testing.T) {
	log, _ := quiet()
	if _, err := httpmw.Assemble(mw.Options{}, httpmw.AccessLog(log)); err == nil {
		t.Fatal("want error: access-log requires request_id")
	}
	if _, err := httpmw.Assemble(mw.Options{}, httpmw.BodyLimit(1), httpmw.RequestID()); err == nil {
		t.Fatal("want error: trace after limits")
	}
}

func gate(status int) httpmw.Member {
	return httpmw.Member{
		Desc: mw.Descriptor{Name: "gate", Phase: mw.PhaseAuthz, Failure: mw.Closed},
		MW: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Ok") == "" {
					httpmw.WriteError(w, svcerr.New(svcerr.CodePermission, "denied", svcerr.WithStatus(status)))
					return
				}
				next.ServeHTTP(w, r)
			})
		},
	}
}

func TestShadowAndEnforce(t *testing.T) {
	var ev []mw.Event
	obs := func(e mw.Event) { ev = append(ev, e) }
	serve := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "served") })

	g := gate(403)
	g.Mode = mw.Shadow
	s, _ := httpmw.Assemble(mw.Options{Observer: obs}, g)
	rr := httptest.NewRecorder()
	s.Then(serve).ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 200 || rr.Body.String() != "served" {
		t.Fatalf("shadow blocked: %d %s", rr.Code, rr.Body)
	}
	if len(ev) != 1 || ev[0].Outcome != mw.Denied || ev[0].Mode != mw.Shadow || !strings.Contains(ev[0].Err.Error(), "403") {
		t.Fatalf("%+v", ev)
	}

	g.Mode = mw.Enforce
	s, _ = httpmw.Assemble(mw.Options{}, g)
	rr = httptest.NewRecorder()
	s.Then(serve).ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 403 {
		t.Fatalf("enforce: %d", rr.Code)
	}
}

func TestShadowBodyStillReadableByHandler(t *testing.T) {
	g := gate(403)
	g.Mode = mw.Shadow
	s, _ := httpmw.Assemble(mw.Options{}, g)
	rr := httptest.NewRecorder()
	s.Then(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	})).ServeHTTP(rr, httptest.NewRequest("POST", "/", strings.NewReader("payload")))
	if rr.Body.String() != "payload" {
		t.Fatalf("body: %q", rr.Body)
	}
}

func TestMiddlewarePanicFailureModes(t *testing.T) {
	boom := func(name string, phase mw.Phase, fm mw.FailureMode) httpmw.Member {
		return httpmw.Member{
			Desc: mw.Descriptor{Name: name, Phase: phase, Failure: fm, Reason: "test"},
			MW: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("mw broke") })
			},
		}
	}
	serve := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "served") })

	s, _ := httpmw.Assemble(mw.Options{}, boom("o", mw.PhaseCache, mw.Open))
	rr := httptest.NewRecorder()
	s.Then(serve).ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Body.String() != "served" {
		t.Fatalf("open: %d %s", rr.Code, rr.Body)
	}

	s, _ = httpmw.Assemble(mw.Options{}, boom("c", mw.PhaseAuthn, mw.Closed))
	rr = httptest.NewRecorder()
	s.Then(serve).ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 500 || strings.Contains(rr.Body.String(), "broke") {
		t.Fatalf("closed: %d %s", rr.Code, rr.Body)
	}
}

func TestCORS(t *testing.T) {
	s, _ := httpmw.Assemble(mw.Options{}, httpmw.CORS(httpmw.CORSConfig{
		Origins: []string{"https://app.example"}, Methods: []string{"GET", "POST"}, Headers: []string{"Authorization"},
	}))
	h := s.Then(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "x") }))

	req := httptest.NewRequest("OPTIONS", "/", nil)
	req.Header.Set("Origin", "https://app.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 204 || rr.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("preflight: %d %v", rr.Code, rr.Header())
	}

	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unknown origin allowed")
	}
}
