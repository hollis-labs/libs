package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A request is judged by the {mode, verifier} generation it was admitted under
// for its whole life, including a later AuthorizeTool call made from inside the
// handler, however long the handler runs and whatever Apply does meanwhile.

func pausedToolCall(t *testing.T, g *Guard, bearer, tool string, during func()) error {
	t.Helper()
	paused, release := make(chan struct{}), make(chan struct{})
	var toolErr error
	done := make(chan struct{})
	h := g.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(paused)
		<-release
		toolErr = g.AuthorizeTool(r.Context(), tool)
	}))
	go func() {
		defer close(done)
		req := httptest.NewRequest("GET", "/notes", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-paused
	during()
	close(release)
	<-done
	return toolErr
}

func TestAuthorizeToolUsesTheGenerationTheRequestWasAdmittedUnder(t *testing.T) {
	// The key holds tesseract.read only; the tool needs tesseract.write.
	reader := NewAPIKeyVerifier(APIKey{Hash: HashKey("reader"), ID: "svc/r", Scopes: []string{"tesseract.read"}})
	scopes := NewScopeTable().Route("GET", "/notes", "tesseract.read").Tool("note_create", "tesseract.write")

	// Admitted under enforce; Apply(off) lands while the handler is paused. The
	// tool call must still be judged by enforce: a missing scope is refused.
	g, err := New(Config{Mode: ModeEnforce, Verifier: reader, Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	err = pausedToolCall(t, g, "reader", "note_create", func() {
		if e := g.Apply(ModeOff, reader); e != nil {
			t.Fatal(e)
		}
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("a request admitted under enforce had its tool call judged by the later mode: %v", err)
	}

	// Admitted under off; Apply(enforce) lands meanwhile. The tool call keeps
	// the mode it started under (off lets the under-scoped call through).
	g, err = New(Config{Mode: ModeOff, Verifier: reader, Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	err = pausedToolCall(t, g, "reader", "note_create", func() {
		if e := g.Apply(ModeEnforce, reader); e != nil {
			t.Fatal(e)
		}
	})
	if err != nil {
		t.Fatalf("a request admitted under off had its tool call judged by the later mode: %v", err)
	}

	// A caller with no pinned generation (stdio: the principal was stamped by
	// the app) is judged by the generation current at the call.
	g, err = New(Config{Mode: ModeEnforce, Verifier: reader, Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPrincipal(context.Background(), Principal{Verified: true, ID: "svc/r", Scopes: []string{"tesseract.read"}})
	if err := g.AuthorizeTool(ctx, "note_create"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unpinned call under enforce: %v", err)
	}
}

// Reloading a verifier must not forget revocations. The library never builds a
// deny-list for the app: the list belongs OUTSIDE the reload, shared by every
// verifier the app builds, with one poller feeding it. This pins that contract.
func TestSwappingVerifiersThatShareADenyListKeepsRevocations(t *testing.T) {
	is := newIssuer(t, "k1")
	dl := NewDenyList()
	build := func() *JWTVerifier {
		return newVerifier(t, is, func(c *JWTConfig) { c.DenyList = dl })
	}
	sw := NewSwitch(build())
	ctx := context.Background()
	tok := is.token("k1", claims(nil))
	if _, err := sw.Verify(ctx, tok); err != nil {
		t.Fatal(err)
	}
	dl.Replace(DenyListDoc{JTI: []string{"j1"}}) // learned from the issuer
	if _, err := sw.Verify(ctx, tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("setup: revoked token accepted: %v", err)
	}
	// Reload with the issuer's deny-list endpoint down: nothing new is learned.
	for i := 0; i < 3; i++ {
		sw.Swap(build())
		if _, err := sw.Verify(ctx, tok); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("reload %d forgot a revocation: %v", i, err)
		}
	}
	// The hazard, spelled out: a verifier built with a fresh list accepts the
	// revoked token again until its first successful poll.
	sw.Swap(newVerifier(t, is, func(c *JWTConfig) { c.DenyList = NewDenyList() }))
	if _, err := sw.Verify(ctx, tok); err != nil {
		t.Fatalf("the documented hazard did not reproduce (a fresh list should not know the revocation): %v", err)
	}
}
