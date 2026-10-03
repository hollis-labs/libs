package pluginhooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDiscoverySectionsAndMethods(t *testing.T) {
	r, _ := fixture(t)
	called := false
	providers := map[string]Provider{
		"hooks": r,
		"contributions": ProviderFunc(func(ctx context.Context) (json.RawMessage, error) {
			called = ctx.Value(testContextKey{}) == "request"
			return json.RawMessage(`{"catalog_version":2,"kinds":[]}`), nil
		}),
		"capabilities": ProviderFunc(func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"catalog_version":1,"capabilities":[]}`), nil
		}),
	}
	h, err := NewDiscoveryHandler(providers)
	if err != nil {
		t.Fatal(err)
	}
	delete(providers, "hooks") // Constructor owns its map.
	req := httptest.NewRequest(http.MethodGet, "/discovery", nil).WithContext(context.WithValue(context.Background(), testContextKey{}, "request"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !called || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	var doc DiscoveryDocument
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.DiscoveryVersion != 1 || len(doc.Catalogs) != 3 || !strings.Contains(string(doc.Catalogs["hooks"]), `"catalog_version":"1"`) {
		t.Fatalf("%+v", doc)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/discovery", nil))
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/discovery", nil))
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal(w.Code, w.Header())
	}
}

type testContextKey struct{}

func TestDiscoveryFailureIsAtomicAndSanitized(t *testing.T) {
	for _, p := range []Provider{
		ProviderFunc(func(context.Context) (json.RawMessage, error) { return nil, errors.New("private-provider-error") }),
		ProviderFunc(func(context.Context) (json.RawMessage, error) { return json.RawMessage(`null`), nil }),
		ProviderFunc(func(context.Context) (json.RawMessage, error) { return json.RawMessage(`[]`), nil }),
		ProviderFunc(func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{invalid`), nil }),
	} {
		h, err := NewDiscoveryHandler(map[string]Provider{"a": ProviderFunc(func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{"private":"partial"}`), nil }), "z": p})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/discovery", nil))
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, providers := range []map[string]Provider{nil, {"bad.name": ProviderFunc(func(context.Context) (json.RawMessage, error) { return nil, nil })}, {"hooks": nil}, {"hooks": ProviderFunc(nil)}} {
		if _, err := NewDiscoveryHandler(providers); err == nil {
			t.Fatal("invalid providers accepted")
		}
	}
	r, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.CatalogSection(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConcurrentCatalogDiscoveryAndWarnings(t *testing.T) {
	d := definition("event.ready", Action)
	d.Deprecated = &Deprecation{Since: "2", Reason: "renamed", Removal: "3"}
	r, err := NewRegistry(Catalog{Version: "1", Definitions: []Definition{d}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.NewScope(ScopeConfig{Owner: "host", Generation: "1", Hooks: []string{d.Name}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewDiscoveryHandler(map[string]Provider{"hooks": r})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 10 {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/discovery", nil))
				if w.Code != http.StatusOK {
					t.Error(w.Code)
				}
				c := r.Catalog()
				c.Definitions[0].Deprecated.Reason = "edit"
				v, err := s.ValidateRegistration(d.Name, "check", Action, Options{})
				if err != nil {
					t.Error(err)
					return
				}
				v.Warnings[0].Deprecation.Reason = "edit"
			}
		})
	}
	wg.Go(func() {
		for range 30 {
			handle, err := s.AddAction(d.Name, "same", Options{}, func(context.Context, Invocation) error { return nil })
			if err != nil {
				t.Error(err)
				return
			}
			regs := s.Registrations()
			if regs[0].Warnings[0].Deprecation.Reason != d.Deprecated.Reason {
				t.Error("warning corrupted")
			}
			regs[0].Warnings[0].Deprecation.Reason = "edit"
			s.Remove(handle)
		}
	})
	wg.Wait()
}
