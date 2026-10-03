package pluginhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
)

// Provider supplies an authorized catalog section for a discovery request.
// Returned bytes must not be mutated during the call; the handler copies them.
// Hosts adapt contribution/capability catalogs without importing their modules
// here. Providers must be concurrency-safe and honor context cancellation.
// A host applies authentication and permitted-subset filtering before exposing
// this handler; a section conveys data, never registration or execution authority.
type Provider interface {
	CatalogSection(context.Context) (json.RawMessage, error)
}

// ProviderFunc adapts a host callback to Provider.
type ProviderFunc func(context.Context) (json.RawMessage, error)

func (f ProviderFunc) CatalogSection(ctx context.Context) (json.RawMessage, error) { return f(ctx) }

// CatalogSection makes a Registry usable as the hooks section provider.
func (r *Registry) CatalogSection(ctx context.Context) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(r.Catalog())
}

// DiscoveryDocument combines independently versioned catalogs. The version of
// this envelope is independent of each section's schema/catalog version.
type DiscoveryDocument struct {
	DiscoveryVersion int                        `json:"discovery_version"`
	Catalogs         map[string]json.RawMessage `json:"catalogs"`
}

type discoveryHandler struct {
	names     []string
	providers map[string]Provider
}

var sectionName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// NewDiscoveryHandler returns one GET/HEAD endpoint with named catalog sections.
// Hosts normally provide hooks, contributions and capabilities and mount it at
// their chosen API path. Omitted sections mean unsupported, never an empty grant.
// The provider map is copied; provider implementations remain host-owned.
// Every request reads sections anew; no cross-module atomic snapshot is promised.
// If any provider fails, the entire request fails without exposing partial data
// or provider error text. Mount behind the host's auth and request-budget policy.
func NewDiscoveryHandler(providers map[string]Provider) (http.Handler, error) {
	if len(providers) == 0 {
		return nil, errors.New("discovery needs at least one provider")
	}
	h := &discoveryHandler{providers: make(map[string]Provider, len(providers))}
	for name, p := range providers {
		if !sectionName.MatchString(name) || p == nil {
			return nil, fmt.Errorf("invalid discovery provider %q", name)
		}
		// Reject a nil ProviderFunc as well as a nil interface. Other typed nil
		// implementations are the host's responsibility, as with other callbacks.
		if f, ok := p.(ProviderFunc); ok && f == nil {
			return nil, fmt.Errorf("nil discovery provider %q", name)
		}
		h.providers[name] = p
		h.names = append(h.names, name)
	}
	sort.Strings(h.names)
	return h, nil
}
func (h *discoveryHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	doc := DiscoveryDocument{DiscoveryVersion: 1, Catalogs: make(map[string]json.RawMessage, len(h.names))}
	for _, name := range h.names {
		section, err := h.providers[name].CatalogSection(req.Context())
		section = slices.Clone(section)
		var object map[string]json.RawMessage
		if err != nil || json.Unmarshal(section, &object) != nil || object == nil {
			http.Error(w, "catalog discovery unavailable", http.StatusInternalServerError)
			return
		}
		doc.Catalogs[name] = section
	}
	b, err := json.Marshal(doc)
	if err != nil {
		http.Error(w, "catalog discovery unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if req.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(b); err != nil {
		return
	} // Response may already be committed; the server owns connection failures.
}
