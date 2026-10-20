package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeIssuer mints "tok-N" valid for ttl and can be told to fail.
type fakeIssuer struct {
	clk   *fakeClock
	ttl   time.Duration
	n     atomic.Int32
	fail  atomic.Bool
	delay chan struct{} // if set, Mint blocks until it is closed
	// entered, if set, receives a value each time Mint is entered, so a test
	// can wait for a mint to be in flight instead of sleeping.
	entered chan struct{}
}

func (f *fakeIssuer) mint(context.Context) (Issued, error) {
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.delay != nil {
		<-f.delay
	}
	f.n.Add(1)
	if f.fail.Load() {
		return Issued{}, errors.New("issuer down")
	}
	return Issued{Token: "tok-" + strconv.Itoa(int(f.n.Load())), ExpiresAt: f.clk.now().Add(f.ttl)}, nil
}

func newRefresher(t *testing.T, ttl time.Duration, mod func(*RefresherConfig)) (*Refresher, *fakeIssuer, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	is := &fakeIssuer{clk: clk, ttl: ttl}
	cfg := RefresherConfig{Mint: is.mint, Now: clk.now}
	if mod != nil {
		mod(&cfg)
	}
	r, err := NewRefresher(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r, is, clk
}

func TestRefresherHoldsTokenAcrossSeveralExpiries(t *testing.T) {
	r, is, clk := newRefresher(t, 15*time.Minute, nil)
	ctx := context.Background()
	prev := ""
	for i := 0; i < 8; i++ { // 8 half-lives of a 15 minute token: well past 3 expiries
		tok, err := r.Token(ctx)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if exp := r.ExpiresAt(); !exp.After(clk.now()) {
			t.Fatalf("step %d: handed out an expired token (exp %v now %v)", i, exp, clk.now())
		}
		prev = tok
		clk.add(7*time.Minute + 30*time.Second)
	}
	if prev == "" || is.n.Load() < 4 {
		t.Fatalf("expected repeated renewal, got %d mints", is.n.Load())
	}
}

func TestRefresherRenewsAtTwoThirdsNotBefore(t *testing.T) {
	r, is, clk := newRefresher(t, 15*time.Minute, nil)
	ctx := context.Background()
	first, _ := r.Token(ctx)
	clk.add(9*time.Minute + 59*time.Second) // just before 10m
	if got, _ := r.Token(ctx); got != first || is.n.Load() != 1 {
		t.Fatalf("renewed early: %q after %d mints", got, is.n.Load())
	}
	clk.add(2 * time.Second) // past 10m
	got, err := r.Token(ctx)
	if err != nil || got == first || is.n.Load() != 2 {
		t.Fatalf("did not renew at 2/3: %q %v mints=%d", got, err, is.n.Load())
	}
}

func TestRefresherServesOldTokenWhileIssuerIsDown(t *testing.T) {
	r, is, clk := newRefresher(t, 15*time.Minute, nil)
	ctx := context.Background()
	first, _ := r.Token(ctx)
	is.fail.Store(true)
	clk.add(11 * time.Minute) // due, but 4m of life left
	got, err := r.Token(ctx)
	if err != nil || got != first {
		t.Fatalf("should keep serving the valid token: %q %v", got, err)
	}
	clk.add(4*time.Minute - 2*time.Second) // inside Skew of expiry
	if _, err := r.Token(ctx); !errors.Is(err, ErrNoToken) || !strings.Contains(err.Error(), "issuer down") {
		t.Fatalf("want ErrNoToken wrapping the mint error, got %v", err)
	}
	is.fail.Store(false)
	clk.add(time.Minute) // backoff has elapsed
	if got, err := r.Token(ctx); err != nil || got == first {
		t.Fatalf("did not recover: %q %v", got, err)
	}
}

func TestRefresherBacksOffAfterFailure(t *testing.T) {
	r, is, clk := newRefresher(t, 15*time.Minute, func(c *RefresherConfig) {
		c.RetryMin, c.RetryMax = 2*time.Second, 8*time.Second
	})
	ctx := context.Background()
	if _, err := r.Token(ctx); err != nil {
		t.Fatal(err)
	}
	is.fail.Store(true)
	clk.add(11 * time.Minute)
	r.Token(ctx) // attempt 1 fails (mints=2)
	r.Token(ctx) // inside backoff: no new attempt
	if is.n.Load() != 2 {
		t.Fatalf("retried inside the backoff window: %d mints", is.n.Load())
	}
	clk.add(2 * time.Second)
	r.Token(ctx) // attempt 2 fails, delay doubles to 4s
	clk.add(3 * time.Second)
	r.Token(ctx)
	if is.n.Load() != 3 {
		t.Fatalf("backoff did not double: %d mints", is.n.Load())
	}
	clk.add(2 * time.Second)
	r.Token(ctx)
	if is.n.Load() != 4 {
		t.Fatalf("no retry after the doubled delay: %d mints", is.n.Load())
	}
}

func TestRefresherConcurrentCallersShareOneMint(t *testing.T) {
	r, is, _ := newRefresher(t, 15*time.Minute, nil)
	is.delay = make(chan struct{})
	is.entered = make(chan struct{}, 1)
	var wg sync.WaitGroup
	toks := make([]string, 16)
	for i := range toks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			toks[i], _ = r.Token(context.Background())
		}()
	}
	<-is.entered // one mint is in flight; callers that arrive later wait for it or find its token
	close(is.delay)
	wg.Wait()
	if is.n.Load() != 1 {
		t.Fatalf("%d mints for 16 concurrent first callers", is.n.Load())
	}
	for _, tok := range toks {
		if tok != toks[0] || tok == "" {
			t.Fatalf("callers saw different tokens: %v", toks)
		}
	}
}

func TestRefresherDueCallerDoesNotBlockOthers(t *testing.T) {
	r, is, clk := newRefresher(t, 15*time.Minute, nil)
	ctx := context.Background()
	first, _ := r.Token(ctx)
	clk.add(11 * time.Minute)
	is.delay = make(chan struct{})
	is.entered = make(chan struct{}, 1)
	done := make(chan string)
	go func() { tok, _ := r.Token(ctx); done <- tok }()
	<-is.entered // the renewing caller is now parked in Mint
	if got, err := r.Token(ctx); err != nil || got != first {
		t.Fatalf("second caller should get the still-valid token without waiting: %q %v", got, err)
	}
	close(is.delay)
	if got := <-done; got == first || got == "" {
		t.Fatalf("renewing caller got %q", got)
	}
}

func TestRefresherRejectsUnusableIssuance(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	r, _ := NewRefresher(RefresherConfig{Now: clk.now, Mint: func(context.Context) (Issued, error) {
		return Issued{Token: "x", ExpiresAt: clk.now().Add(-time.Second)}, nil
	}})
	if _, err := r.Token(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("already-expired issuance accepted: %v", err)
	}
	if _, err := NewRefresher(RefresherConfig{}); err == nil {
		t.Fatal("missing Mint accepted")
	}
	if _, err := NewRefresher(RefresherConfig{Mint: func(context.Context) (Issued, error) { return Issued{}, nil }, Fraction: 1}); err == nil {
		t.Fatal("fraction 1 accepted")
	}
}

func TestRefresherOnRefreshNeverSeesTheToken(t *testing.T) {
	var got []string
	r, is, clk := newRefresher(t, 15*time.Minute, func(c *RefresherConfig) {
		c.OnRefresh = func(exp time.Time, err error) {
			if err != nil {
				got = append(got, "err")
			} else {
				got = append(got, "ok")
			}
		}
	})
	r.Token(context.Background())
	is.fail.Store(true)
	clk.add(11 * time.Minute)
	r.Token(context.Background())
	if strings.Join(got, ",") != "ok,err" {
		t.Fatalf("callbacks: %v", got)
	}
}

func TestRefresherRunRenewsAheadOfDemand(t *testing.T) {
	// Real clock, tiny lifetime: Run alone must keep minting.
	var n atomic.Int32
	r, err := NewRefresher(RefresherConfig{
		Mint: func(context.Context) (Issued, error) {
			n.Add(1)
			return Issued{Token: "t", ExpiresAt: time.Now().Add(150 * time.Millisecond)}, nil
		},
		Skew: time.Millisecond, RetryMin: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	time.Sleep(700 * time.Millisecond)
	cancel()
	<-done
	if n.Load() < 4 {
		t.Fatalf("Run minted only %d times in 700ms for a 150ms token", n.Load())
	}
}

func TestHTTPMinterTetherShape(t *testing.T) {
	var gotAuth, gotProto, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotProto = r.Header.Get("Authorization"), r.Header.Get("Tether-Protocol")
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		gotBody = b["audience"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "jwt-1", "ttl_seconds": 900})
	}))
	defer srv.Close()
	now := time.Unix(1_800_000_000, 0)
	m := &HTTPMinter{URL: srv.URL, Body: map[string]any{"audience": "torque", "scopes": []string{"torque.read"}},
		Credential: func() (string, error) { return "parent-secret", nil },
		Header:     http.Header{"Tether-Protocol": {"1"}}, Now: func() time.Time { return now }}
	got, err := m.Mint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer parent-secret" || gotProto != "1" || gotBody != "torque" {
		t.Fatalf("bad request: auth=%q proto=%q aud=%q", gotAuth, gotProto, gotBody)
	}
	if got.Token != "jwt-1" || !got.ExpiresAt.Equal(now.Add(900*time.Second)) {
		t.Fatalf("bad issued %+v", got)
	}
}

func TestHTTPMinterExpiryFallbacksAndErrors(t *testing.T) {
	is := newIssuer(t, "k1")
	jwt := is.token("k1", claims(map[string]any{"exp": time.Now().Add(15 * time.Minute).Unix()}))
	replies := map[string]struct {
		status int
		body   string
		ok     bool
	}{
		"expires_at": {200, `{"token":"a","expires_at":"2030-01-01T00:00:00Z"}`, true},
		"jwt exp":    {200, `{"token":"` + jwt + `"}`, true},
		"no expiry":  {200, `{"token":"opaque"}`, false},
		"refused":    {403, `{"error":"scope"}`, false},
		"garbage":    {200, `not json`, false},
		"empty":      {200, `{}`, false},
	}
	for name, rep := range replies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(rep.status)
			_, _ = w.Write([]byte(rep.body))
		}))
		m := &HTTPMinter{URL: srv.URL, Credential: func() (string, error) { return "parent-secret", nil }}
		_, err := m.Mint(context.Background())
		srv.Close()
		if (err == nil) != rep.ok {
			t.Errorf("%s: err=%v want ok=%v", name, err, rep.ok)
		}
		if err != nil && strings.Contains(err.Error(), "parent-secret") {
			t.Errorf("%s: error leaks the credential: %v", name, err)
		}
	}
}

func TestHTTPMinterRefusesCrossOriginRedirectAndHidesURL(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect target was contacted (auth %q)", r.Header.Get("Authorization"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	m := &HTTPMinter{URL: srv.URL + "/?key=url-secret", Credential: func() (string, error) { return "parent-secret", nil }}
	_, err := m.Mint(context.Background())
	if err == nil || strings.Contains(err.Error(), "url-secret") || strings.Contains(err.Error(), "parent-secret") {
		t.Fatalf("want an error without secrets, got %v", err)
	}
	m2 := &HTTPMinter{URL: srv.URL, Credential: func() (string, error) { return "", errors.New("no file") }}
	if _, err := m2.Mint(context.Background()); err == nil {
		t.Fatal("credential error ignored")
	}
}

func TestRefresherWithHTTPMinterEndToEnd(t *testing.T) {
	// A real verifier accepts every token the refresher hands out.
	is := newIssuer(t, "k1")
	v := newVerifier(t, is, nil)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		exp := time.Now().Add(2 * time.Second)
		tok := is.token("k1", claims(map[string]any{"exp": exp.Unix(), "jti": "j" + strconv.Itoa(int(n.Load()))}))
		_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_at": exp})
	}))
	defer srv.Close()
	m := &HTTPMinter{URL: srv.URL, Credential: func() (string, error) { return "parent", nil }}
	r, err := NewRefresher(RefresherConfig{Mint: m.Mint, Skew: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && n.Load() < 3 {
		tok, err := r.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := v.Verify(context.Background(), tok); err != nil {
			t.Fatalf("verifier rejected a token the refresher handed out: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if n.Load() < 3 {
		t.Fatalf("only %d mints across 5s of 2s tokens", n.Load())
	}
}

func TestRefresherCanceledWaiterReturnsWhileAMintIsStalled(t *testing.T) {
	r, is, _ := newRefresher(t, 15*time.Minute, nil)
	is.delay = make(chan struct{})
	is.entered = make(chan struct{}, 1)
	first := make(chan error, 1)
	go func() { _, err := r.Token(context.Background()); first <- err }()
	<-is.entered // the first mint is stalled inside the issuer

	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() { _, err := r.Token(ctx); second <- err }()
	cancel()
	select {
	case err := <-second:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiter got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a canceled caller stayed blocked behind a stalled mint")
	}
	close(is.delay)
	if err := <-first; err != nil {
		t.Fatalf("stalled mint should still complete: %v", err)
	}
	if is.n.Load() != 1 {
		t.Fatalf("canceled waiter caused a mint: %d total", is.n.Load())
	}
}

func TestRefresherKeepsOldTokenWhenIssuanceExpiresBeforeItArrives(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	var n atomic.Int32
	var errs []error
	r, err := NewRefresher(RefresherConfig{Now: clk.now, OnRefresh: func(_ time.Time, err error) { errs = append(errs, err) },
		Mint: func(context.Context) (Issued, error) {
			if n.Add(1) == 1 {
				return Issued{Token: "old", ExpiresAt: clk.now().Add(15 * time.Minute)}, nil
			}
			// Valid when minted (now+1s) but the clock passes it before the
			// reply is handled.
			iss := Issued{Token: "stale", ExpiresAt: clk.now().Add(time.Second)}
			clk.add(2 * time.Second)
			return iss, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if tok, _ := r.Token(ctx); tok != "old" {
		t.Fatalf("first token %q", tok)
	}
	clk.add(11 * time.Minute) // due, 4m of life left
	tok, err := r.Token(ctx)
	if err != nil || tok != "old" {
		t.Fatalf("an already-expired issuance replaced a valid token: %q %v", tok, err)
	}
	if len(errs) != 2 || errs[0] != nil || errs[1] == nil {
		t.Fatalf("OnRefresh results: %v", errs)
	}
	before := n.Load()
	r.Token(ctx) // inside the backoff window: no hammering
	if n.Load() != before {
		t.Fatalf("retried inside the backoff window")
	}
}

func TestRefresherIssuanceInsideSkewIsUnusable(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	r, _ := NewRefresher(RefresherConfig{Now: clk.now, Skew: 5 * time.Second,
		Mint: func(context.Context) (Issued, error) {
			return Issued{Token: "short", ExpiresAt: clk.now().Add(3 * time.Second)}, nil
		}})
	if _, err := r.Token(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("a token already inside Skew was handed out: %v", err)
	}
}

func TestHTTPMinterErrorsNeverEchoTheResponseBodyCredentialOrURL(t *testing.T) {
	// An endpoint that reflects the Authorization header into its error body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("denied: " + r.Header.Get("Authorization") + " " + r.URL.RawQuery)) //nolint:gosec // test endpoint that deliberately reflects secrets
	}))
	defer srv.Close()
	cred := func() (string, error) { return "parent-secret", nil }
	for name, url := range map[string]string{
		"reflected body": srv.URL + "/?key=url-secret",
		"invalid url":    "http://exa mple.invalid/?key=url-secret",
		"control char":   "http://127.0.0.1:1/\x7f?key=url-secret",
		"bad scheme":     "ht!tp://127.0.0.1/?key=url-secret",
	} {
		m := &HTTPMinter{URL: url, Credential: cred}
		_, err := m.Mint(context.Background())
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		for _, secret := range []string{"parent-secret", "url-secret", "Bearer"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: error leaks %q: %v", name, secret, err)
			}
		}
	}
	// The same through the Refresher: OnRefresh and ErrNoToken carry the error.
	var seen []string
	r, _ := NewRefresher(RefresherConfig{Mint: (&HTTPMinter{URL: srv.URL + "/?key=url-secret", Credential: cred}).Mint,
		OnRefresh: func(_ time.Time, err error) { seen = append(seen, err.Error()) }})
	_, err := r.Token(context.Background())
	if err == nil || strings.Contains(err.Error(), "parent-secret") || strings.Contains(err.Error(), "url-secret") {
		t.Fatalf("refresher error: %v", err)
	}
	for _, m := range seen {
		if strings.Contains(m, "parent-secret") || strings.Contains(m, "url-secret") {
			t.Fatalf("OnRefresh error leaks: %s", m)
		}
	}
}
