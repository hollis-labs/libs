package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const aud = "tesseract"

type issuer struct {
	keys map[string]ed25519.PrivateKey
	srv  *httptest.Server
	hits atomic.Int32
	down atomic.Bool
}

func newIssuer(t *testing.T, kids ...string) *issuer {
	t.Helper()
	is := &issuer{keys: map[string]ed25519.PrivateKey{}}
	for _, k := range kids {
		is.add(k)
	}
	is.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		is.hits.Add(1)
		if is.down.Load() {
			http.Error(w, "down", 500)
			return
		}
		var ks []map[string]string
		for kid, priv := range is.keys {
			ks = append(ks, map[string]string{"kty": "OKP", "crv": "Ed25519", "use": "sig", "kid": kid,
				"x": base64.RawURLEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": ks})
	}))
	t.Cleanup(is.srv.Close)
	return is
}

func (is *issuer) add(kid string) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	is.keys[kid] = priv
}

func (is *issuer) token(kid string, claims map[string]any) string {
	return signJWT(is.keys[kid], kid, "EdDSA", claims)
}

func signJWT(priv ed25519.PrivateKey, kid, alg string, claims map[string]any) string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	in := enc(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"}) + "." + enc(claims)
	return in + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(in)))
}

func claims(over map[string]any) map[string]any {
	c := map[string]any{"iss": "https://tether", "sub": "msg://agent/a1", "aud": aud, "scope": "tesseract.read tesseract.write",
		"exp": time.Now().Add(10 * time.Minute).Unix(), "iat": time.Now().Unix(), "jti": "j1", "kind": "agent", "sid": "s1"}
	for k, v := range over {
		c[k] = v
	}
	return c
}

func newVerifier(t *testing.T, is *issuer, mod func(*JWTConfig)) *JWTVerifier {
	t.Helper()
	cfg := JWTConfig{Issuer: "https://tether", Audience: aud, JWKSURL: is.srv.URL, MinRefresh: time.Nanosecond}
	if mod != nil {
		mod(&cfg)
	}
	v, err := NewJWTVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestJWTValid(t *testing.T) {
	is := newIssuer(t, "k1")
	p, err := newVerifier(t, is, nil).Verify(context.Background(), is.token("k1", claims(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "msg://agent/a1" || p.Kind != "agent" || p.SessionID != "s1" || !p.HasScope("tesseract.write") || p.HasScope("tesseract.admin") {
		t.Fatalf("bad principal %+v", p)
	}
}

func TestJWTRejects(t *testing.T) {
	is := newIssuer(t, "k1")
	v := newVerifier(t, is, nil)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	cases := map[string]string{
		"expired":        is.token("k1", claims(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})),
		"wrong audience": is.token("k1", claims(map[string]any{"aud": "torque"})),
		"aud array":      is.token("k1", claims(map[string]any{"aud": []string{aud}})),
		"aud array two":  is.token("k1", claims(map[string]any{"aud": []string{aud, "torque"}})),
		"wrong issuer":   is.token("k1", claims(map[string]any{"iss": "evil"})),
		"no exp":         is.token("k1", claims(map[string]any{"exp": 0})),
		"bad signature":  signJWT(other, "k1", "EdDSA", claims(nil)),
		"alg none":       signJWT(is.keys["k1"], "k1", "none", claims(nil)),
		"alg hs256":      signJWT(is.keys["k1"], "k1", "HS256", claims(nil)),
		"unknown kid":    signJWT(other, "nope", "EdDSA", claims(nil)),
		"garbage":        "a.b.c",
		"two parts":      "a.b",
	}
	for name, tok := range cases {
		if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: want ErrInvalidToken, got %v", name, err)
		}
	}
}

func TestJWKSRotation(t *testing.T) {
	is := newIssuer(t, "k1")
	v := newVerifier(t, is, nil)
	ctx := context.Background()
	if _, err := v.Verify(ctx, is.token("k1", claims(nil))); err != nil {
		t.Fatal(err)
	}
	// Overlap window: issuer publishes k2 alongside k1; k2 token is picked up
	// on demand without waiting for the periodic refresh.
	is.add("k2")
	if _, err := v.Verify(ctx, is.token("k2", claims(nil))); err != nil {
		t.Fatalf("new kid not fetched on demand: %v", err)
	}
	if _, err := v.Verify(ctx, is.token("k1", claims(nil))); err != nil {
		t.Fatalf("old kid should work during overlap: %v", err)
	}
	// Overlap ends: k1 removed; after refresh its tokens fail.
	old := is.token("k1", claims(nil))
	delete(is.keys, "k1")
	if err := v.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, old); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("rotated-out kid must fail, got %v", err)
	}
}

func TestJWKSOnDemandRefetchRateLimited(t *testing.T) {
	is := newIssuer(t, "k1")
	now := time.Now()
	v := newVerifier(t, is, func(c *JWTConfig) { c.MinRefresh = time.Minute; c.Now = func() time.Time { return now } })
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	for range 5 {
		_, _ = v.Verify(context.Background(), signJWT(other, "nope", "EdDSA", claims(nil)))
	}
	if h := is.hits.Load(); h != 1 {
		t.Fatalf("unknown-kid flood must not hammer the issuer: %d fetches", h)
	}
}

func TestBootstrapKeyWhenIssuerDown(t *testing.T) {
	is := newIssuer(t, "k1")
	is.down.Store(true)
	dir := t.TempDir()

	pub := is.keys["k1"].Public().(ed25519.PublicKey)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	pemPath := filepath.Join(dir, "pub.pem")
	_ = os.WriteFile(pemPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600)
	jwksPath := filepath.Join(dir, "jwks.json")
	_ = os.WriteFile(jwksPath, []byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"k1","x":"`+base64.RawURLEncoding.EncodeToString(pub)+`"}]}`), 0o600)

	for _, path := range []string{pemPath, jwksPath} {
		v := newVerifier(t, is, func(c *JWTConfig) { c.BootstrapKeyFile = path })
		if _, err := v.Verify(context.Background(), is.token("k1", claims(nil))); err != nil {
			t.Fatalf("%s: bootstrap key should verify while issuer is down: %v", filepath.Base(path), err)
		}
	}
	// No bootstrap and issuer down -> unavailable, not invalid.
	v := newVerifier(t, is, nil)
	if _, err := v.Verify(context.Background(), is.token("k1", claims(nil))); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestRevocationDenyList(t *testing.T) {
	is := newIssuer(t, "k1")
	dl := NewDenyList()
	v := newVerifier(t, is, func(c *JWTConfig) { c.DenyList = dl })
	ctx := context.Background()
	tok := is.token("k1", claims(nil))
	if _, err := v.Verify(ctx, tok); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]DenyListDoc{"jti": {JTI: []string{"j1"}}, "sub": {Sub: []string{"msg://agent/a1"}}, "sid": {SID: []string{"s1"}}} {
		dl.Replace(doc)
		if _, err := v.Verify(ctx, tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s revoked token accepted: %v", name, err)
		}
	}
	dl.Replace(DenyListDoc{SID: []string{"other"}})
	if _, err := v.Verify(ctx, tok); err != nil {
		t.Fatalf("unrelated deny entry rejected token: %v", err)
	}
}

func TestDenyListPollerETag(t *testing.T) {
	var gets, conditional atomic.Int32
	doc := DenyListDoc{SID: []string{"s1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer srv.Close()
	dl := NewDenyList()
	p := &DenyListPoller{URL: srv.URL, List: dl}
	ctx := context.Background()
	if err := p.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if !dl.Denied(Principal{SessionID: "s1"}) || dl.Denied(Principal{SessionID: "s2"}) {
		t.Fatal("deny list not applied")
	}
	if err := p.Poll(ctx); err != nil || conditional.Load() != 1 {
		t.Fatalf("second poll should be conditional 304: err=%v cond=%d", err, conditional.Load())
	}
	if !dl.Denied(Principal{SessionID: "s1"}) {
		t.Fatal("304 must keep list")
	}
	srv.Close()
	if err := p.Poll(ctx); err == nil {
		t.Fatal("want error from dead issuer")
	}
	if !dl.Denied(Principal{SessionID: "s1"}) {
		t.Fatal("failed poll must keep last list")
	}
}

func TestAPIKeyVerifier(t *testing.T) {
	v := NewAPIKeyVerifier(
		APIKey{Hash: HashKey("secret-1"), ID: "svc/tesseract-root", Scopes: []string{"tesseract.*"}},
		APIKey{Hash: HashKey("old"), ID: "svc/old", ExpiresAt: time.Now().Add(-time.Hour)},
	)
	p, err := v.Verify(context.Background(), "secret-1")
	if err != nil || p.Kind != KindService || !p.HasScope("tesseract.write") || p.HasScope("torque.write") {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []string{"nope", "old", ""} {
		if _, err := v.Verify(context.Background(), bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestParseBearer(t *testing.T) {
	cases := []struct {
		h     []string
		token string
		state string
	}{
		{nil, "", StateMissing},
		{[]string{"Bearer abc"}, "abc", ""},
		{[]string{"bearer abc"}, "abc", ""},
		{[]string{"Basic abc"}, "", StateInvalid},
		{[]string{"Bearer"}, "", StateInvalid},
		{[]string{"Bearer a b"}, "", StateInvalid},
		{[]string{"Bearer a", "Bearer b"}, "", StateInvalid},
		{[]string{"Bearer " + strings.Repeat("x", 5000)}, "", StateInvalid},
	}
	for _, c := range cases {
		tok, st := ParseBearer(c.h)
		if tok != c.token || st != c.state {
			t.Errorf("%v: got %q %q", c.h, tok, st)
		}
	}
}

func TestMalformedHeaderThroughMiddleware(t *testing.T) {
	g, _ := New(Config{Mode: ModeEnforce, Verifier: NewAPIKeyVerifier()})
	h := g.Middleware(okHandler())
	for _, hdr := range []string{"Basic x", "Bearer", "Bearer a b", "garbage"} {
		rec := do(h, "GET", "/x", hdr)
		if rec.Code != 401 {
			t.Errorf("%q: %d", hdr, rec.Code)
		}
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := FromContext(r.Context()); ok {
			w.Header().Set("X-Principal", p.ID)
			if p.Verified {
				w.Header().Set("X-Verified", "1")
			}
		}
		w.WriteHeader(200)
	})
}

func do(h http.Handler, method, path, authz string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func guardFixture(t *testing.T, mode Mode) (http.Handler, *[]Event) {
	t.Helper()
	var evs []Event
	scopes := NewScopeTable().
		Route("GET", "/notes/{id}", "tesseract.read").
		Route("POST", "/notes", "tesseract.write").
		Route("*", "/admin/*", "tesseract.admin").
		Tool("note_create", "tesseract.write")
	v := NewAPIKeyVerifier(
		APIKey{Hash: HashKey("reader"), ID: "svc/r", Scopes: []string{"tesseract.read"}},
		APIKey{Hash: HashKey("root"), ID: "svc/root", Scopes: []string{"tesseract.*"}},
	)
	g, err := New(Config{Mode: mode, Verifier: v, Scopes: scopes, Audit: func(e Event) { evs = append(evs, e) }})
	if err != nil {
		t.Fatal(err)
	}
	return g.Middleware(okHandler()), &evs
}

func TestEnforce(t *testing.T) {
	h, _ := guardFixture(t, ModeEnforce)
	for _, c := range []struct {
		method, path, authz string
		want                int
	}{
		{"GET", "/notes/7", "", 401},
		{"GET", "/notes/7", "Bearer bad", 401},
		{"GET", "/notes/7", "Bearer reader", 200},
		{"POST", "/notes", "Bearer reader", 403},
		{"POST", "/notes", "Bearer root", 200},
		{"GET", "/admin/x/y", "Bearer reader", 403},
		{"GET", "/unmapped", "Bearer reader", 403}, // fail closed
		{"GET", "/health", "", 200},
	} {
		if got := do(h, c.method, c.path, c.authz).Code; got != c.want {
			t.Errorf("%s %s %q: got %d want %d", c.method, c.path, c.authz, got, c.want)
		}
	}
}

func TestEnforceUnmappedOptOutAndDefault(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*ScopeTable)
		want int
	}{
		"allow unmapped": {func(t *ScopeTable) { t.AllowUnmapped = true }, 200},
		"default scope":  {func(t *ScopeTable) { t.Default = "tesseract.admin" }, 403},
	} {
		st := NewScopeTable()
		tc.set(st)
		g, err := New(Config{Mode: ModeEnforce, Verifier: NewAPIKeyVerifier(APIKey{Hash: HashKey("r"), ID: "r", Scopes: []string{"tesseract.read"}}), Scopes: st})
		if err != nil {
			t.Fatal(err)
		}
		if got := do(g.Middleware(okHandler()), "GET", "/x", "Bearer r").Code; got != tc.want {
			t.Errorf("%s: got %d want %d", name, got, tc.want)
		}
	}
	h, _ := guardFixture(t, ModePermissive)
	if c := do(h, "GET", "/unmapped", "Bearer reader").Code; c != 200 {
		t.Errorf("permissive must not reject unmapped: %d", c)
	}
}

func TestPermissiveNeverRejectsAndLogsWouldDeny(t *testing.T) {
	h, evs := guardFixture(t, ModePermissive)
	if c := do(h, "POST", "/notes", "Bearer reader").Code; c != 200 {
		t.Fatalf("permissive rejected: %d", c)
	}
	if c := do(h, "GET", "/notes/1", "").Code; c != 200 {
		t.Fatalf("permissive rejected anonymous: %d", c)
	}
	e := *evs
	if len(e) != 2 || !e[0].WouldDeny || e[0].RequiredScope != "tesseract.write" || !e[1].WouldDeny || e[0].Denied {
		t.Fatalf("events %+v", e)
	}
}

func TestOffModeSemanticsB(t *testing.T) {
	h, evs := guardFixture(t, ModeOff)
	rec := do(h, "POST", "/notes", "")
	if rec.Code != 200 || rec.Header().Get("X-Principal") != OffModePrincipalID || rec.Header().Get("X-Verified") != "" {
		t.Fatalf("anonymous off: %d %v", rec.Code, rec.Header())
	}
	if (*evs)[0].Authentication != StateOffMode {
		t.Fatalf("missing off-mode audit: %+v", (*evs)[0])
	}
	// Invalid token is never rejected either.
	if c := do(h, "GET", "/notes/1", "Bearer bad").Code; c != 200 {
		t.Fatal(c)
	}
	// A valid token is still honored for attribution.
	rec = do(h, "GET", "/notes/1", "Bearer reader")
	if rec.Header().Get("X-Principal") != "svc/r" || rec.Header().Get("X-Verified") != "1" {
		t.Fatalf("off mode must honor token: %v", rec.Header())
	}
}

func TestVerifierUnavailable(t *testing.T) {
	g, _ := New(Config{Mode: ModeEnforce, Verifier: failing{}})
	if c := do(g.Middleware(okHandler()), "GET", "/x", "Bearer t").Code; c != 503 {
		t.Fatalf("got %d", c)
	}
	g, _ = New(Config{Mode: ModePermissive, Verifier: failing{}})
	if c := do(g.Middleware(okHandler()), "GET", "/x", "Bearer t").Code; c != 200 {
		t.Fatalf("got %d", c)
	}
}

type failing struct{}

func (failing) Verify(context.Context, string) (Principal, error) {
	return Principal{}, errors.New("db down")
}

func TestAuthorizeTool(t *testing.T) {
	scopes := NewScopeTable().Tool("note_create", "tesseract.write").Tool("note_get", "tesseract.read")
	g, _ := New(Config{Mode: ModeEnforce, Verifier: NewAPIKeyVerifier(), Scopes: scopes})
	reader := WithPrincipal(context.Background(), Principal{Verified: true, ID: "r", Scopes: []string{"tesseract.read"}})
	if err := g.AuthorizeTool(reader, "note_get"); err != nil {
		t.Fatal(err)
	}
	if err := g.AuthorizeTool(reader, "note_create"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v", err)
	}
	if err := g.AuthorizeTool(context.Background(), "note_get"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("got %v", err)
	}
	gp, _ := New(Config{Mode: ModePermissive, Scopes: scopes})
	if err := gp.AuthorizeTool(reader, "note_create"); err != nil {
		t.Fatalf("permissive: %v", err)
	}
	goff, _ := New(Config{Mode: ModeOff, Scopes: scopes})
	if err := goff.AuthorizeTool(context.Background(), "note_create"); err != nil {
		t.Fatalf("off: %v", err)
	}
}

func TestEndToEndJWTThroughMiddleware(t *testing.T) {
	is := newIssuer(t, "k1")
	g, err := New(Config{Mode: ModeEnforce, Verifier: newVerifier(t, is, nil), Scopes: NewScopeTable().Route("GET", "/notes/{id}", "tesseract.read")})
	if err != nil {
		t.Fatal(err)
	}
	h := g.Middleware(okHandler())
	if c := do(h, "GET", "/notes/1", "Bearer "+is.token("k1", claims(nil))).Code; c != 200 {
		t.Fatal(c)
	}
	if c := do(h, "GET", "/notes/1", "Bearer "+is.token("k1", claims(map[string]any{"scope": "other.read"}))).Code; c != 403 {
		t.Fatal(c)
	}
	if c := do(h, "GET", "/notes/1", "Bearer "+is.token("k1", claims(map[string]any{"aud": "x"}))).Code; c != 401 {
		t.Fatal(c)
	}
}

func TestValidateBind(t *testing.T) {
	for _, c := range []struct {
		addr string
		mode Mode
		ok   bool
	}{
		{"127.0.0.1:8080", ModeOff, true},
		{"tcp:127.0.0.1:8080", ModePermissive, true},
		{"localhost:80", ModeOff, true},
		{"[::1]:80", ModeOff, true},
		{"100.101.102.103:80", ModeOff, true},
		{"[fd7a:115c:a1e0::1]:80", ModePermissive, true},
		{"host.tail1234.ts.net:80", ModeOff, true},
		{"unix:/run/x.sock", ModeOff, true},
		{"0.0.0.0:8080", ModeOff, false},
		{":8080", ModePermissive, false},
		{"192.168.1.5:80", ModeOff, false},
		{"tcp:10.0.0.1:80", ModePermissive, false},
		{"0.0.0.0:8080", ModeEnforce, true},
		{":8080", ModeEnforce, true},
		{"127.0.0.1:80", Mode("bogus"), false},
		{"nonsense", ModeOff, false},
	} {
		if err := ValidateBind(c.addr, c.mode); (err == nil) != c.ok {
			t.Errorf("%s %s: err=%v want ok=%v", c.addr, c.mode, err, c.ok)
		}
	}
}

func TestModeParsingAndEnv(t *testing.T) {
	if m, err := ParseMode("Observe"); err != nil || m != ModePermissive {
		t.Fatal(m, err)
	}
	if _, err := ParseMode(""); err == nil {
		t.Fatal("empty must be invalid")
	}
	t.Setenv("HOLLIS_AUTH", "off")
	if m, _ := ModeFromEnv("tether", ModeEnforce); m != ModeOff {
		t.Fatal(m)
	}
	t.Setenv("HOLLIS_AUTH_GO_X", "enforce")
	if m, _ := ModeFromEnv("go-x", ModeOff); m != ModeEnforce {
		t.Fatal(m)
	}
	t.Setenv("HOLLIS_AUTH", "")
	t.Setenv("HOLLIS_AUTH_GO_X", "")
	if m, _ := ModeFromEnv("go-x", ModePermissive); m != ModePermissive {
		t.Fatal(m)
	}
	if _, err := New(Config{Mode: ""}); err == nil {
		t.Fatal("empty mode must fail New")
	}
	g, _ := New(Config{Mode: ModeOff})
	if g.Status() != "auth=off" {
		t.Fatal(g.Status())
	}
}

func TestHasScopeWildcards(t *testing.T) {
	p := Principal{Scopes: []string{"tether.*", "torque.read"}}
	if !p.HasScope("tether.operate") || !p.HasScope("torque.read") || p.HasScope("torque.write") || p.HasScope("tetherx.read") {
		t.Fatal("wildcard semantics")
	}
	if !(Principal{Scopes: []string{"*"}}).HasScope("anything.x") {
		t.Fatal("star")
	}
}

func TestChain(t *testing.T) {
	is := newIssuer(t, "k1")
	c := Chain(NewAPIKeyVerifier(APIKey{Hash: HashKey("root"), ID: "root"}), newVerifier(t, is, nil))
	if _, err := c.Verify(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Verify(context.Background(), is.token("k1", claims(nil))); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Verify(context.Background(), "zzz"); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
}

func TestOnRequestSeesRequestPrincipalAndEvent(t *testing.T) {
	var gotPath, gotID, gotAuth string
	g, err := New(Config{Mode: ModeEnforce, Verifier: NewAPIKeyVerifier(APIKey{Hash: HashKey("k"), ID: "svc/k", Scopes: []string{"*"}}),
		OnRequest: func(r *http.Request, p Principal, ev Event) {
			gotPath, gotID, gotAuth = r.URL.Path, p.ID, ev.Authentication
		}})
	if err != nil {
		t.Fatal(err)
	}
	do(g.Middleware(okHandler()), "GET", "/x", "Bearer k")
	if gotPath != "/x" || gotID != "svc/k" || gotAuth != StateVerified {
		t.Fatalf("got %q %q %q", gotPath, gotID, gotAuth)
	}
}
