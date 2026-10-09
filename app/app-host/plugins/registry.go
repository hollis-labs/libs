// Package plugins adapts host-admitted shared registry DTOs to same-origin delivery.
package plugins

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sync"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/registry"
)

// Delivery holds immutable reviewed bytes for one registry snapshot. No path is
// resolved from an HTTP request, and no process/environment authority is implied.
type Delivery struct {
	mu          sync.RWMutex
	response    []byte
	assets      map[string]asset
	revision    uint64
	instance    string
	generations map[string]string
}
type asset struct {
	bytes       []byte
	contentType string
}
type Bundle struct {
	JavaScript []byte
	Stylesheet []byte
}

func NewDelivery(response registry.Response, bundles map[string]Bundle, runtimes map[string]string) (*Delivery, error) {
	d := &Delivery{}
	if err := d.Replace(response, bundles, runtimes); err != nil {
		return nil, err
	}
	return d, nil
}

// Replace validates and snapshots all bytes before atomically replacing authority.
// Old URLs must contain owner generation/version and become unavailable on revoke.
func (d *Delivery) Replace(response registry.Response, bundles map[string]Bundle, runtimes map[string]string) error {
	if err := response.Validate(); err != nil {
		return err
	}
	if _, err := response.Plan(registry.AdmissionPolicy{Kinds: response.Kinds, Regions: response.Regions}); err != nil {
		return err
	}
	assets := map[string]asset{}
	generations := map[string]string{}
	add := func(url, contentType string, b []byte) error {
		if url == "" {
			return nil
		}
		if len(b) == 0 {
			return fmt.Errorf("plugins: declared asset has no reviewed bytes")
		}
		if _, exists := assets[url]; exists {
			return fmt.Errorf("plugins: duplicate asset URL")
		}
		assets[url] = asset{append([]byte(nil), b...), contentType}
		return nil
	}
	for owner, p := range response.Plugins {
		bundle, exists := bundles[owner]
		if p.BundleURL == "" {
			if exists {
				return fmt.Errorf("plugins: undeclared bundle")
			}
			continue
		}
		if owner == "." || owner == ".." || p.OwnerGeneration == "." || p.OwnerGeneration == ".." || !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(p.OwnerGeneration) {
			return fmt.Errorf("plugins: unsafe generation")
		}
		generations[owner+"/"+p.OwnerGeneration] = registry.BundleDigest(bundle.JavaScript) + ":" + registry.BundleDigest(bundle.Stylesheet)
		prefix := "/plugins/" + owner + "/" + p.OwnerGeneration + "/"
		if p.BundleURL != prefix+"bundle.js" || (p.StylesheetURL != "" && p.StylesheetURL != prefix+"style.css") {
			return fmt.Errorf("plugins: asset URL must bind owner generation")
		}
		if err := registry.VerifyBundle(p, bundle.JavaScript, runtimes, false); err != nil {
			return err
		}
		if err := add(p.BundleURL, "text/javascript; charset=utf-8", bundle.JavaScript); err != nil {
			return err
		}
		if err := add(p.StylesheetURL, "text/css; charset=utf-8", bundle.Stylesheet); err != nil {
			return err
		}
	}
	bytes, err := json.Marshal(response)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.instance != "" && (response.HostInstance != d.instance || response.Revision <= d.revision) {
		return fmt.Errorf("plugins: stale registry snapshot")
	}
	for identity, digest := range generations {
		if old, exists := d.generations[identity]; exists && old != digest {
			return fmt.Errorf("plugins: generation bytes changed")
		}
	}
	if d.generations == nil {
		d.generations = map[string]string{}
	}
	for identity, digest := range generations {
		d.generations[identity] = digest
	}
	d.instance = response.HostInstance
	d.revision = response.Revision
	d.response = bytes
	d.assets = assets
	return nil
}
func (d *Delivery) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "read-only", http.StatusMethodNotAllowed)
		return
	}
	d.mu.RLock()
	response := d.response
	item, ok := d.assets[r.URL.Path]
	d.mu.RUnlock()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/plugins/registry" {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "HEAD" {
			_, _ = w.Write(response)
		}
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", item.contentType)
	if r.Method != "HEAD" {
		_, _ = w.Write(item.bytes)
	}
}
