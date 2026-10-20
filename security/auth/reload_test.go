package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startReloader(t *testing.T, r *Reloader) {
	t.Helper()
	r.NoSignal = r.NoSignal || !strings.Contains(t.Name(), "SIGHUP")
	if r.Interval == 0 {
		r.Interval = 10 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(30 * time.Millisecond) // let Run record the startup content
}

func TestSwitchSwapAndEmpty(t *testing.T) {
	s := NewSwitch(nil)
	if _, err := s.Verify(context.Background(), "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty switch: %v", err)
	}
	is := newIssuer(t, "k1")
	v := newVerifier(t, is, nil)
	if old := s.Swap(v); old != nil {
		t.Fatalf("old = %v", old)
	}
	if _, err := s.Verify(context.Background(), is.token("k1", claims(nil))); err != nil {
		t.Fatal(err)
	}
	if old := s.Swap(nil); old != Verifier(v) {
		t.Fatalf("swap did not return the previous verifier")
	}
	if _, err := s.Verify(context.Background(), is.token("k1", claims(nil))); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("cleared switch: %v", err)
	}
}

func TestReloaderAppliesChangeOnceAndIgnoresTouch(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.conf")
	writeFile(t, p, "v1")
	var n atomic.Int32
	startReloader(t, &Reloader{Path: p, Load: func(context.Context) error { n.Add(1); return nil }})
	writeFile(t, p, "v1") // same content: not a change
	time.Sleep(80 * time.Millisecond)
	if n.Load() != 0 {
		t.Fatalf("reloaded on identical content: %d", n.Load())
	}
	writeFile(t, p, "v2")
	waitFor(t, "reload", func() bool { return n.Load() == 1 })
	time.Sleep(80 * time.Millisecond)
	if n.Load() != 1 {
		t.Fatalf("reloaded %d times for one change", n.Load())
	}
}

func TestReloaderFailedLoadKeepsOldAndIsNotRetriedUntilFixed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.conf")
	writeFile(t, p, "good")
	var mu sync.Mutex
	var results []error
	var attempts atomic.Int32
	current := "good"
	startReloader(t, &Reloader{Path: p,
		Load: func(context.Context) error {
			attempts.Add(1)
			b, _ := os.ReadFile(p) //nolint:gosec // test path
			if string(b) == "broken" {
				return errors.New("parse error")
			}
			mu.Lock()
			current = string(b)
			mu.Unlock()
			return nil
		},
		OnResult: func(err error) { mu.Lock(); results = append(results, err); mu.Unlock() }})
	writeFile(t, p, "broken")
	waitFor(t, "failed attempt", func() bool { return attempts.Load() == 1 })
	time.Sleep(100 * time.Millisecond)
	if attempts.Load() != 1 {
		t.Fatalf("broken file retried %d times", attempts.Load())
	}
	mu.Lock()
	if current != "good" || len(results) != 1 || results[0] == nil {
		t.Fatalf("current=%q results=%v", current, results)
	}
	mu.Unlock()
	writeFile(t, p, "fixed")
	waitFor(t, "recovery", func() bool { mu.Lock(); defer mu.Unlock(); return current == "fixed" })
}

func TestReloaderWaitsForAStableFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.conf")
	writeFile(t, p, "v0")
	var seen []string
	var mu sync.Mutex
	startReloader(t, &Reloader{Path: p, Interval: 40 * time.Millisecond, Load: func(context.Context) error {
		b, _ := os.ReadFile(p) //nolint:gosec // test path
		mu.Lock()
		seen = append(seen, string(b))
		mu.Unlock()
		return nil
	}})
	// Rapid successive writes (an editor saving in pieces): only the final
	// content may be loaded, never an intermediate one.
	for _, v := range []string{"a", "ab", "abc"} {
		writeFile(t, p, v)
		time.Sleep(10 * time.Millisecond)
	}
	waitFor(t, "load", func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) > 0 })
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "abc" {
		t.Fatalf("loaded %v, want only [abc]", seen)
	}
}

func TestReloaderMissingFileMidReplaceIsNotAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.conf")
	writeFile(t, p, "v1")
	var n atomic.Int32
	startReloader(t, &Reloader{Path: p, Load: func(context.Context) error { n.Add(1); return nil }})
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	writeFile(t, p, "v2")
	waitFor(t, "reload after replace", func() bool { return n.Load() == 1 })
}

func TestReloaderTriggerAndPanicRecovery(t *testing.T) {
	var n atomic.Int32
	var got atomic.Value
	r := &Reloader{Load: func(context.Context) error {
		if n.Add(1) == 1 {
			panic("boom")
		}
		return nil
	}, OnResult: func(err error) { got.Store(err != nil) }}
	r.Trigger() // before Run: remembered, not lost, not blocking
	startReloader(t, r)
	waitFor(t, "first trigger", func() bool { return n.Load() == 1 })
	waitFor(t, "panic reported", func() bool { v, _ := got.Load().(bool); return v })
	r.Trigger()
	waitFor(t, "second trigger", func() bool { return n.Load() == 2 })
	waitFor(t, "recovered", func() bool { v, _ := got.Load().(bool); return !v })
}

func TestGuardSetMode(t *testing.T) {
	v := NewAPIKeyVerifier(APIKey{Hash: HashKey("k"), ID: "svc", Scopes: []string{"tesseract.read"}})
	scopes := NewScopeTable().Route("GET", "/notes", "tesseract.read")
	g, err := New(Config{Mode: ModeOff, Verifier: v, Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	h := g.Middleware(okHandler())
	if got := do(h, "GET", "/notes", "").Code; got != 200 {
		t.Fatalf("off: %d", got)
	}
	if err := g.SetMode(ModeEnforce); err != nil {
		t.Fatal(err)
	}
	if g.Mode() != ModeEnforce || g.Status() != "auth=enforce" {
		t.Fatalf("mode %v", g.Mode())
	}
	if got := do(h, "GET", "/notes", "").Code; got != 401 {
		t.Fatalf("enforce after SetMode: %d", got)
	}
	if got := do(h, "GET", "/notes", "Bearer k").Code; got != 200 {
		t.Fatalf("enforce with key: %d", got)
	}
	if err := g.SetMode(ModePermissive); err != nil || do(h, "GET", "/notes", "").Code != 200 {
		t.Fatalf("permissive after SetMode: %v", err)
	}
	if err := g.SetMode("bogus"); err == nil || g.Mode() != ModePermissive {
		t.Fatalf("bogus mode accepted or applied: %v %v", err, g.Mode())
	}
}

func TestGuardSetModeRefusals(t *testing.T) {
	g, err := New(Config{Mode: ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	if err = g.SetMode(ModeEnforce); err == nil || g.Mode() != ModeOff {
		t.Fatalf("enforce without a verifier accepted: %v", err)
	}
	pub, err := New(Config{Mode: ModeEnforce, Verifier: NewSwitch(nil), Addr: "0.0.0.0:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.SetMode(ModeOff); err == nil || pub.Mode() != ModeEnforce {
		t.Fatalf("loosening a non-local listener accepted: %v", err)
	}
	local, _ := New(Config{Mode: ModeEnforce, Verifier: NewSwitch(nil), Addr: "127.0.0.1:8080"})
	if err := local.SetMode(ModeOff); err != nil || local.Mode() != ModeOff {
		t.Fatalf("loopback loosening refused: %v", err)
	}
}

func TestGuardSetModeRace(t *testing.T) {
	g, _ := New(Config{Mode: ModeOff, Verifier: NewSwitch(nil), Scopes: NewScopeTable().Route("GET", "/x", "a.read")})
	h := g.Middleware(okHandler())
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					do(h, "GET", "/x", "Bearer nope")
					_ = g.AuthorizeTool(context.Background(), "t")
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		_ = g.SetMode([]Mode{ModeOff, ModePermissive, ModeEnforce}[i%3])
	}
	close(stop)
	wg.Wait()
}

func pemPub(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// The end state this exists for: an app serving requests picks up a rotated
// issuer key and a posture change from its config file without a restart.
func TestReloadEndToEndKeyRotationAndPostureFlip(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "issuer.pub.pem")
	modeFile := filepath.Join(dir, "mode")
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)
	writeFile(t, keyFile, pemPub(t, pubA))
	writeFile(t, modeFile, "off")

	sw := NewSwitch(nil)
	scopes := NewScopeTable().Route("GET", "/notes", "tesseract.read")
	g, err := New(Config{Mode: ModeOff, Verifier: sw, Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	load := func(context.Context) error {
		mb, err := os.ReadFile(modeFile) //nolint:gosec // test path
		if err != nil {
			return err
		}
		mode, err := ParseMode(strings.TrimSpace(string(mb)))
		if err != nil {
			return err
		}
		jv, err := NewJWTVerifier(JWTConfig{Issuer: "https://tether", Audience: aud, BootstrapKeyFile: keyFile})
		if err != nil {
			return err
		}
		sw.Swap(jv)
		return g.SetMode(mode)
	}
	if err := load(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloaded := make(chan error, 8)
	startReloader(t, &Reloader{Path: keyFile, Load: load, OnResult: func(err error) { reloaded <- err }})

	h := g.Middleware(okHandler())
	tokA := "Bearer " + signJWT(privA, "k", "EdDSA", claims(nil))
	tokB := "Bearer " + signJWT(privB, "k", "EdDSA", claims(nil))

	if got := do(h, "GET", "/notes", tokA).Code; got != 200 {
		t.Fatalf("token A before rotation: %d", got)
	}
	writeFile(t, modeFile, "enforce")
	writeFile(t, keyFile, pemPub(t, pubB)) // rotate the issuer key
	if err := <-reloaded; err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if g.Mode() != ModeEnforce {
		t.Fatalf("mode after reload: %v", g.Mode())
	}
	if got := do(h, "GET", "/notes", tokB).Code; got != 200 {
		t.Fatalf("token B after rotation: %d", got)
	}
	if got := do(h, "GET", "/notes", tokA).Code; got != 401 {
		t.Fatalf("token A after rotation under enforce: %d", got)
	}
	// A broken edit keeps the working configuration.
	writeFile(t, modeFile, "bogus")
	writeFile(t, keyFile, "not a key")
	if err := <-reloaded; err == nil {
		t.Fatal("broken config reported as success")
	}
	if g.Mode() != ModeEnforce || do(h, "GET", "/notes", tokB).Code != 200 {
		t.Fatal("broken config disturbed the running configuration")
	}
}

// --- review findings on mode normalization, atomic apply, request snapshot and digest ordering ---

func keyVerifier(key string) Verifier {
	return NewAPIKeyVerifier(APIKey{Hash: HashKey(key), ID: "svc/" + key, Scopes: []string{"tesseract.read"}})
}

func notesGuard(t *testing.T, cfg Config) (*Guard, http.Handler) {
	t.Helper()
	cfg.Scopes = NewScopeTable().Route("GET", "/notes", "tesseract.read")
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return g, g.Middleware(okHandler())
}

func TestModesAreStoredCanonicalNotAsGiven(t *testing.T) {
	// New: a non-canonical spelling must still enforce, not fall through every case.
	g, h := notesGuard(t, Config{Mode: "ENFORCE", Verifier: keyVerifier("k")})
	if g.Mode() != ModeEnforce || g.Status() != "auth=enforce" {
		t.Fatalf("New stored %q", g.Mode())
	}
	if got := do(h, "GET", "/notes", "").Code; got != 401 {
		t.Fatalf("anonymous request under Mode=ENFORCE: %d", got)
	}
	// SetMode: the reviewer's repro. Start enforcing, flip to "ENFORCE".
	g, h = notesGuard(t, Config{Mode: ModeEnforce, Verifier: keyVerifier("k")})
	if err := g.SetMode("ENFORCE"); err != nil || g.Mode() != ModeEnforce {
		t.Fatalf("SetMode(ENFORCE): %v %q", err, g.Mode())
	}
	if got := do(h, "GET", "/notes", "").Code; got != 401 {
		t.Fatalf("anonymous request after SetMode(ENFORCE): %d", got)
	}
	for in, want := range map[Mode]Mode{" observe ": ModePermissive, "Permissive": ModePermissive, "OFF": ModeOff, "enforce": ModeEnforce} {
		if err := g.SetMode(in); err != nil || g.Mode() != want {
			t.Errorf("SetMode(%q): err=%v mode=%q want %q", in, err, g.Mode(), want)
		}
	}
	if err := g.SetMode("enforcing"); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

func TestApplyIsAtomicAndRefusalChangesNothing(t *testing.T) {
	oldV, newV := keyVerifier("old"), keyVerifier("new")
	g, h := notesGuard(t, Config{Mode: ModeEnforce, Verifier: oldV, Addr: "0.0.0.0:8080"})
	accepts := func(key string) bool { return do(h, "GET", "/notes", "Bearer "+key).Code == 200 }
	if !accepts("old") || accepts("new") {
		t.Fatal("setup")
	}
	// Loosening a non-local listener is refused; the verifier must not move either.
	if err := g.Apply(ModeOff, newV); err == nil {
		t.Fatal("off on a non-local bind accepted")
	}
	if err := g.CheckApply(ModeOff, newV); err == nil {
		t.Fatal("CheckApply disagrees with Apply")
	}
	if g.Mode() != ModeEnforce || !accepts("old") || accepts("new") {
		t.Fatalf("a refused Apply changed state: mode=%q old=%v new=%v", g.Mode(), accepts("old"), accepts("new"))
	}
	if err := g.Apply("bogus", newV); err == nil || !accepts("old") || accepts("new") {
		t.Fatalf("a bogus mode changed state: %v", err)
	}
	// A valid pair lands together.
	if err := g.Apply(ModeEnforce, newV); err != nil {
		t.Fatal(err)
	}
	if accepts("old") || !accepts("new") {
		t.Fatal("verifier not replaced by a valid Apply")
	}
	// A nil verifier keeps the current one.
	if err := g.Apply(ModeEnforce, nil); err != nil || !accepts("new") {
		t.Fatalf("Apply(mode, nil): %v", err)
	}
}

func TestApplyRefusesEnforceWithoutAVerifier(t *testing.T) {
	g, _ := notesGuard(t, Config{Mode: ModeOff})
	if err := g.Apply(ModeEnforce, nil); err == nil || g.Mode() != ModeOff {
		t.Fatalf("enforce without any verifier accepted: %v mode=%q", err, g.Mode())
	}
	if err := g.Apply(ModeEnforce, keyVerifier("k")); err != nil || g.Mode() != ModeEnforce {
		t.Fatalf("enforce with a verifier refused: %v", err)
	}
}

// blockingVerifier holds Verify until released, so a request can be in flight
// while the Guard is reconfigured.
type blockingVerifier struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingVerifier) Verify(context.Context, string) (Principal, error) {
	b.entered <- struct{}{}
	<-b.release
	return Principal{}, ErrInvalidToken
}

func TestRequestKeepsTheModeAndVerifierItStartedWith(t *testing.T) {
	bv := &blockingVerifier{entered: make(chan struct{}, 1), release: make(chan struct{})}
	g, h := notesGuard(t, Config{Mode: ModeOff, Verifier: bv})
	done := make(chan int, 1)
	go func() { done <- do(h, "GET", "/notes", "Bearer some-token").Code }()
	<-bv.entered // the request is inside verification, already decided to run under "off"
	if err := g.Apply(ModeEnforce, keyVerifier("k")); err != nil {
		t.Fatal(err)
	}
	close(bv.release)
	if got := <-done; got != 200 {
		t.Fatalf("an in-flight request was judged under the new mode: %d", got)
	}
	if got := do(h, "GET", "/notes", "Bearer some-token").Code; got != 401 {
		t.Fatalf("a new request should run under the new pair: %d", got)
	}
}

func TestReloaderEditDuringLoadGetsItsOwnPass(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.conf")
	writeFile(t, p, "v1")
	var mu sync.Mutex
	var loaded []string
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	r := &Reloader{Path: p, Load: func(context.Context) error {
		b, _ := os.ReadFile(p) //nolint:gosec // test path
		mu.Lock()
		loaded = append(loaded, string(b))
		first := len(loaded) == 1
		mu.Unlock()
		entered <- struct{}{}
		if first {
			<-release // stall the first Load while the file is edited
		}
		return nil
	}}
	startReloader(t, r)
	r.Trigger()
	<-entered // the first Load is running (it read v1)
	writeFile(t, p, "v2")
	close(release)
	waitFor(t, "a second pass for the edit made during Load", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(loaded) == 2 && loaded[1] == "v2"
	})
}
