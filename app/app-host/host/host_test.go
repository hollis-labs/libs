package host

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"
)

func TestMounting(t *testing.T) {
	for _, base := range []string{"", "lab", "/lab/"} {
		h, err := New(Config{Name: "test", BasePath: base, Assets: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("app")}}, Routes: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.URL.Path) })})
		if err != nil {
			t.Fatal(err)
		}
		root := "/"
		if base != "" {
			root = "/lab/"
		}
		for _, entry := range []struct {
			path string
			code int
			body string
		}{{root + "details", 200, "app"}, {root + "missing.js", 404, "404 page not found\n"}, {"/api/fixture", 200, "/api/fixture"}, {"/plugins/registry", 404, "404 page not found\n"}} {
			recorder := httptest.NewRecorder()
			h.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", entry.path, nil))
			if recorder.Code != entry.code || recorder.Body.String() != entry.body {
				t.Fatalf("%s: %d %q", entry.path, recorder.Code, recorder.Body.String())
			}
		}
	}
	for _, base := range []string{"api", "/api/child", "plugins", "healthz", "/a/../b", "/{x}"} {
		if _, err := New(Config{Name: "test", BasePath: base}); err == nil {
			t.Fatalf("accepted reserved/invalid %q", base)
		}
	}
}
func TestPlaceholder(t *testing.T) {
	h, _ := New(Config{Name: "test"})
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatal("missing placeholder")
	}
}
func TestHTTPDrainsBeforeFreshStopBudget(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	stopped := make(chan error, 1)
	h, _ := New(Config{Name: "test", ShutdownTimeout: time.Second, Routes: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }), Stop: func(ctx context.Context) error { stopped <- ctx.Err(); return nil }})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx, listener) }()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, err := http.Get("http://" + listener.Addr().String() + "/api/slow")
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	<-entered
	cancel()
	select {
	case <-stopped:
		t.Fatal("stop preceded active HTTP drain")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatal("expired cleanup context", err)
	}
	<-clientDone
}
