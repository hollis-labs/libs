package httpsink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
)

func ev(id string) runtimeevents.Event {
	return runtimeevents.Event{SchemaVersion: runtimeevents.SchemaVersion, ID: id, Kind: "session.ready", Time: time.Unix(1, 0).UTC(), Subject: "msg://test/x"}
}

type recorder struct {
	mu    sync.Mutex
	ids   []string
	auth  string
	calls atomic.Int32
}

func (r *recorder) handler(status func(call int32) int) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		n := r.calls.Add(1)
		if st := status(n); st != http.StatusOK {
			w.WriteHeader(st)
			return
		}
		var body struct {
			Events []runtimeevents.Event `json:"events"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		r.auth = req.Header.Get("Authorization")
		for _, e := range body.Events {
			r.ids = append(r.ids, e.ID)
		}
		r.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}
}

func closeSink(t *testing.T, s *Sink) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestSendsBatchWithToken(t *testing.T) {
	r := &recorder{}
	srv := httptest.NewServer(r.handler(func(int32) int { return 200 }))
	defer srv.Close()
	s, err := New(Config{URL: srv.URL, Token: "tok", FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		_ = s.Write(context.Background(), ev(id))
	}
	closeSink(t, s)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.ids) != 3 || r.ids[0] != "a" || r.ids[2] != "c" {
		t.Fatalf("ids = %v", r.ids)
	}
	if r.auth != "Bearer tok" {
		t.Fatalf("auth = %q", r.auth)
	}
	if st := s.Stats(); st.Sent != 3 || st.Dropped != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestRetriesTransientThenSucceeds(t *testing.T) {
	r := &recorder{}
	srv := httptest.NewServer(r.handler(func(n int32) int {
		if n < 3 {
			return http.StatusServiceUnavailable
		}
		return 200
	}))
	defer srv.Close()
	s, _ := New(Config{URL: srv.URL, FlushInterval: 10 * time.Millisecond, BackoffBase: time.Millisecond})
	_ = s.Write(context.Background(), ev("a"))
	deadline := time.Now().Add(5 * time.Second)
	for s.Stats().Sent == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	closeSink(t, s)
	if st := s.Stats(); st.Sent != 1 || st.Retries != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestRejectedIsNotRetried(t *testing.T) {
	r := &recorder{}
	srv := httptest.NewServer(r.handler(func(int32) int { return http.StatusBadRequest }))
	defer srv.Close()
	var drops atomic.Int32
	s, _ := New(Config{URL: srv.URL, FlushInterval: time.Hour, BackoffBase: time.Millisecond, OnDrop: func(n int, reason string) {
		if reason == "rejected" {
			drops.Add(int32(n))
		}
	}})
	_ = s.Write(context.Background(), ev("a"))
	closeSink(t, s)
	if r.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", r.calls.Load())
	}
	if st := s.Stats(); st.Rejected != 1 || st.Sent != 0 || drops.Load() != 1 {
		t.Fatalf("stats = %+v drops=%d", st, drops.Load())
	}
}

func TestQueueFullDropsOldest(t *testing.T) {
	block := make(chan struct{})
	r := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-block
		r.handler(func(int32) int { return 200 })(w, req)
	}))
	defer srv.Close()
	defer close(block)
	s, _ := New(Config{URL: srv.URL, QueueSize: 2, BatchSize: 1, FlushInterval: time.Hour, Timeout: 50 * time.Millisecond, MaxAttempts: 1})
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		_ = s.Write(context.Background(), ev(id))
	}
	if st := s.Stats(); st.Dropped < 2 {
		t.Fatalf("expected drops, stats = %+v", st)
	}
}

func TestWriteNeverBlocksOnDeadHub(t *testing.T) {
	s, _ := New(Config{URL: "http://127.0.0.1:1", Timeout: 20 * time.Millisecond, BackoffBase: time.Millisecond, MaxAttempts: 2, FlushInterval: 5 * time.Millisecond})
	start := time.Now()
	for i := 0; i < 50; i++ {
		_ = s.Write(context.Background(), ev("x"))
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Write blocked for %v", d)
	}
	closeSink(t, s)
}

func TestFromEnvNoopWhenUnset(t *testing.T) {
	t.Setenv(EnvURL, "")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(Noop); !ok {
		t.Fatalf("got %T, want Noop", c)
	}
	if err := c.Write(context.Background(), ev("a")); err != nil {
		t.Fatal(err)
	}
}

func TestFromEnvReadsTokenFile(t *testing.T) {
	r := &recorder{}
	srv := httptest.NewServer(r.handler(func(int32) int { return 200 }))
	defer srv.Close()
	f := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(f, []byte("filetok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvURL, srv.URL)
	t.Setenv(EnvAuth, "")
	t.Setenv(EnvAuthFile, f)
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Write(context.Background(), ev("a"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Close(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.auth != "Bearer filetok" {
		t.Fatalf("auth = %q", r.auth)
	}
}

func TestNewRequiresURL(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("want error")
	}
}
