package tether

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type environmentFakeClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*environmentFakeTimer
	created chan *environmentFakeTimer
}
type environmentFakeTimer struct {
	clock   *environmentFakeClock
	channel chan time.Time
	when    time.Time
	delay   time.Duration
	stopped bool
}

func newEnvironmentFakeClock() *environmentFakeClock {
	return &environmentFakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), created: make(chan *environmentFakeTimer, 100)}
}
func (c *environmentFakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *environmentFakeClock) NewTimer(d time.Duration) EnvironmentTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &environmentFakeTimer{clock: c, channel: make(chan time.Time, 1), when: c.now.Add(d), delay: d}
	c.timers = append(c.timers, timer)
	c.created <- timer
	return timer
}
func (t *environmentFakeTimer) C() <-chan time.Time { return t.channel }
func (t *environmentFakeTimer) Stop()               { t.clock.mu.Lock(); t.stopped = true; t.clock.mu.Unlock() }
func (c *environmentFakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for _, timer := range c.timers {
		if !timer.stopped && !timer.when.After(c.now) {
			timer.stopped = true
			timer.channel <- c.now
		}
	}
}
func (c *environmentFakeClock) nextTimer(t *testing.T, want time.Duration) {
	t.Helper()
	select {
	case timer := <-c.created:
		if timer.delay != want {
			t.Fatalf("timer=%v want=%v", timer.delay, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timer was not scheduled")
	}
}

func nextEnvironmentUpdate(t *testing.T, s *EnvironmentSubscription, matches func(EnvironmentUpdate) bool) EnvironmentUpdate {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case update, ok := <-s.Updates:
			if !ok {
				t.Fatalf("subscription closed: %v", <-s.Errors)
			}
			if matches(update) {
				return update
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for environment update")
		}
	}
}
func closeEnvironmentSubscription(t *testing.T, s *EnvironmentSubscription) {
	t.Helper()
	s.Close()
	select {
	case <-s.Done:
	case <-time.After(3 * time.Second):
		t.Error("subscription reader did not exit")
	}
}
func writeEnvironmentTestEvent(w http.ResponseWriter, seq int, session string) {
	_, _ = fmt.Fprintf(w, "id: %d\nevent: future.kind\ndata: {\"environment_id\":\"env-test\",\"seq\":%d,\"id\":\"event-%d\",\"kind\":\"future.kind\",\"session_id\":%q,\"payload\":{\"value\":%d}}\n\n", seq, seq, seq, session, seq)
}
func writeEnvironmentTestSync(w http.ResponseWriter, seq int) {
	_, _ = fmt.Fprintf(w, "id: %d\nevent: synchronized\ndata: {\"environment_id\":\"env-test\",\"seq\":%d}\n\n", seq, seq)
}

func TestEnvironmentBackoffDropResumeAndStableReset(t *testing.T) {
	clock := newEnvironmentFakeClock()
	var streams atomic.Int32
	release := make(chan struct{})
	srv := testEnvironmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		index := int(streams.Add(1))
		if r.URL.Path != "/environment/events" || r.URL.Query().Get("after_seq") != fmt.Sprint(index-1) {
			t.Errorf("wrong resume URL %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeEnvironmentTestEvent(w, index, "")
		writeEnvironmentTestSync(w, index)
		w.(http.Flusher).Flush()
		if index == 3 {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	})
	zero := int64(0)
	s, err := testEnvironment(t, []string{srv.URL}, EnvironmentOptions{Clock: clock, Jitter: func() float64 { return 1 }}).Subscribe(context.Background(), EnvironmentStreamOptions{AfterSeq: &zero})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEnvironmentSubscription(t, s)
	for index := 1; index <= 3; index++ {
		update := nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.Event != nil })
		if update.Event.Seq != int64(index) {
			t.Fatalf("repeated/missing event %d", update.Event.Seq)
		}
		nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Freshness == "fresh" })
		if index < 3 {
			nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Transport == "disconnected" })
			delay := time.Second * time.Duration(index)
			clock.nextTimer(t, delay)
			clock.Advance(delay)
		}
	}
	clock.Advance(30 * time.Second)
	close(release)
	nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Transport == "disconnected" })
	clock.nextTimer(t, time.Second)
}

func TestEnvironmentAuthenticationAndOfflineWaitForWake(t *testing.T) {
	clock := newEnvironmentFakeClock()
	var allowed atomic.Bool
	var authCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == EnvironmentDescriptorPath {
			writeTestDescriptor(w, "env-test", 1)
			return
		}
		if r.URL.Path == "/auth/context" {
			authCalls.Add(1)
			if !allowed.Load() {
				w.WriteHeader(http.StatusUnauthorized)
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeEnvironmentTestSync(w, 0)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	zero := int64(0)
	s, err := testEnvironment(t, []string{srv.URL}, EnvironmentOptions{Clock: clock}).Subscribe(context.Background(), EnvironmentStreamOptions{AfterSeq: &zero})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEnvironmentSubscription(t, s)
	nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Transport == "blocked" })
	clock.Advance(time.Hour)
	select {
	case <-clock.created:
		t.Fatal("auth failure scheduled a retry")
	default:
	}
	s.SetOnline(false)
	nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Transport == "offline" })
	if authCalls.Load() != 1 {
		t.Fatal("retried authentication without wake")
	}
	allowed.Store(true)
	s.SetOnline(true)
	nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Freshness == "fresh" })
	if authCalls.Load() != 2 {
		t.Fatal("wakeup did not refresh authentication")
	}
	s.SetOnline(false)
	nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Transport == "offline" })
	clock.Advance(time.Hour)
	if authCalls.Load() != 2 {
		t.Fatal("offline attempted connection")
	}
}

func TestEnvironmentFallbackPreflightCooldown(t *testing.T) {
	clock := newEnvironmentFakeClock()
	var available atomic.Bool
	var preferredAuth atomic.Int32
	preferred := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == EnvironmentDescriptorPath {
			writeTestDescriptor(w, "env-test", 1)
			return
		}
		if r.URL.Path == "/auth/context" {
			preferredAuth.Add(1)
			if !available.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeEnvironmentTestSync(w, 0)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer preferred.Close()
	fallback := testEnvironmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeEnvironmentTestSync(w, 0)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	zero := int64(0)
	s, err := testEnvironment(t, []string{preferred.URL, fallback.URL}, EnvironmentOptions{Clock: clock}).Subscribe(context.Background(), EnvironmentStreamOptions{AfterSeq: &zero})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEnvironmentSubscription(t, s)
	update := nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Freshness == "fresh" })
	if update.State.RouteIndex != 1 {
		t.Fatal("did not use fallback")
	}
	clock.nextTimer(t, time.Minute)
	available.Store(true)
	clock.Advance(time.Minute)
	clock.nextTimer(t, time.Minute)
	if preferredAuth.Load() != 1 {
		t.Fatal("preflight ignored cooldown")
	}
	clock.Advance(4 * time.Minute)
	update = nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Freshness == "fresh" && u.State.RouteIndex == 0 })
	if preferredAuth.Load() != 2 {
		t.Fatal("preferred route not authenticated before switching")
	}
}

func TestEnvironmentNetworkWakeAndFailedPromotion(t *testing.T) {
	clock := newEnvironmentFakeClock()
	var available atomic.Bool
	var preferredAuth, preferredStreams atomic.Int32
	preferred := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == EnvironmentDescriptorPath {
			if !available.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			writeTestDescriptor(w, "env-test", 1)
			return
		}
		if r.URL.Path == "/auth/context" {
			preferredAuth.Add(1)
			return
		}
		preferredStreams.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer preferred.Close()
	fallback := testEnvironmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeEnvironmentTestSync(w, 0)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	zero := int64(0)
	s, err := testEnvironment(t, []string{preferred.URL, fallback.URL}, EnvironmentOptions{Clock: clock, Jitter: func() float64 { return 1 }}).Subscribe(context.Background(), EnvironmentStreamOptions{AfterSeq: &zero})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEnvironmentSubscription(t, s)
	nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Freshness == "fresh" })
	clock.nextTimer(t, time.Minute)
	available.Store(true)
	s.Wake()
	nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Transport == "disconnected" })
	clock.nextTimer(t, time.Second)
	clock.Advance(time.Second)
	update := nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Freshness == "fresh" })
	if update.State.RouteIndex != 1 || preferredAuth.Load() != 1 || preferredStreams.Load() != 1 {
		t.Fatal("failed promotion did not return to fallback")
	}
	clock.nextTimer(t, time.Minute)
	s.Wake()
	clock.Advance(time.Minute)
	clock.nextTimer(t, time.Minute)
	if preferredAuth.Load() != 1 {
		t.Fatal("failed promoted route was retried during cooldown")
	}
}

func TestEnvironmentBackoffJitterAndCap(t *testing.T) {
	client := testEnvironment(t, []string{"http://example.test"}, EnvironmentOptions{Jitter: func() float64 { return 1.5 }})
	if client.retryDelay(0) != 1500*time.Millisecond || client.retryDelay(1) != 3*time.Second || client.retryDelay(20) != 5*time.Minute {
		t.Fatal("incorrect jittered exponential backoff")
	}
}

func TestEnvironmentOfflineInterruptsPreferredRouteProbe(t *testing.T) {
	var probing atomic.Bool
	started := make(chan struct{}, 1)
	preferred := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !probing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer preferred.Close()
	fallback := testEnvironmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeEnvironmentTestSync(w, 0)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	zero := int64(0)
	s, err := testEnvironment(t, []string{preferred.URL, fallback.URL}, EnvironmentOptions{ProbeTimeout: time.Minute}).Subscribe(context.Background(), EnvironmentStreamOptions{AfterSeq: &zero})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEnvironmentSubscription(t, s)
	nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Freshness == "fresh" })
	probing.Store(true)
	s.Wake()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("preferred preflight did not start")
	}
	s.SetOnline(false)
	nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Transport == "offline" })
}

func TestEnvironmentPreferredIdentityMismatchKeepsVerifiedFallback(t *testing.T) {
	clock := newEnvironmentFakeClock()
	var credentials, streams atomic.Int32
	preferred := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			credentials.Add(1)
		}
		writeTestDescriptor(w, "wrong-environment", 1)
	}))
	defer preferred.Close()
	fallback := testEnvironmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		streams.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeEnvironmentTestSync(w, 0)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	zero := int64(0)
	s, err := testEnvironment(t, []string{preferred.URL, fallback.URL}, EnvironmentOptions{Clock: clock}).Subscribe(context.Background(), EnvironmentStreamOptions{AfterSeq: &zero})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEnvironmentSubscription(t, s)
	update := nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Freshness == "fresh" })
	if update.State.RouteIndex != 1 {
		t.Fatal("verified fallback not used")
	}
	clock.nextTimer(t, time.Minute)
	clock.Advance(time.Minute)
	clock.nextTimer(t, time.Minute)
	select {
	case <-s.Done:
		t.Fatal("wrong preferred environment terminated valid fallback")
	default:
	}
	if credentials.Load() != 0 || streams.Load() != 1 {
		t.Fatal("credential sent to wrong environment or working stream replaced")
	}
}
