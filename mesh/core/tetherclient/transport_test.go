package tether

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnixTransportHealth(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "muxd.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		writeTestJSON(t, w, Health{Status: "ok"})
	})}
	defer srv.Close()
	go func() { _ = srv.Serve(ln) }()

	c := MustNew("unix:" + socketPath)
	got, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got.Status != "ok" {
		t.Fatalf("health = %+v", got)
	}
}

func TestTransportForTCPAndHTTP(t *testing.T) {
	if baseURL, _, err := transportFor("tcp:127.0.0.1:7180"); err != nil || baseURL != "http://127.0.0.1:7180" {
		t.Fatalf("tcp transport = %q, %v", baseURL, err)
	}
	if baseURL, _, err := transportFor("http://127.0.0.1:7180"); err != nil || baseURL != "http://127.0.0.1:7180" {
		t.Fatalf("http transport = %q, %v", baseURL, err)
	}
}

func TestExpandListenAddr(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	got := expandListenAddr("unix:~/.tether/run/muxd.sock")
	want := "unix:" + filepath.Join(home, ".tether/run/muxd.sock")
	if got != want {
		t.Fatalf("expandListenAddr = %q, want %q", got, want)
	}
}

func TestLongLivedSessionOperationsUseCallerContextNotTransportTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions/s1/attach":
			time.Sleep(20 * time.Millisecond)
			_, _ = w.Write([]byte("attached"))
		case "/sessions/s1/wait":
			time.Sleep(20 * time.Millisecond)
			writeTestJSON(t, w, WaitResponse{ExitCode: 0})
		case "/sessions/s1/turn":
			time.Sleep(20 * time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
		case "/events/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			time.Sleep(20 * time.Millisecond)
			_, _ = w.Write([]byte("id: 1\n"))
			_, _ = w.Write([]byte("event: daemon.ready\n"))
			_, _ = w.Write([]byte("data: {\"scope\":\"daemon\"}\n\n"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := MustNew(srv.URL)
	c.http.Timeout = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := c.SendTurn(ctx, "s1", "hello"); err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if code, err := c.WaitSession(ctx, "s1"); err != nil || code != 0 {
		t.Fatalf("WaitSession = %d, %v", code, err)
	}
	var buf bytes.Buffer
	if err := c.AttachSession(ctx, "s1", &buf, 0); err != nil || buf.String() != "attached" {
		t.Fatalf("AttachSession = %q, %v", buf.String(), err)
	}
	events, errs := c.StreamEvents(ctx, StreamEventsOptions{Scopes: []string{ScopeDaemon}})
	select {
	case ev := <-events:
		if ev.Kind != "daemon.ready" {
			t.Fatalf("event = %+v", ev)
		}
	case err := <-errs:
		t.Fatalf("stream err = %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
}
