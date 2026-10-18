package modelsdev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"
)

const (
	defaultURL = "https://models.dev/api.json?type=all"
	defaultTTL = 24 * time.Hour
	maxRetries = 3
)

// Client fetches and caches the models.dev catalog.
// All fields are private; use New and the functional options to configure.
type Client struct {
	url                string
	cacheDir           string
	ttl                time.Duration
	httpClient         *http.Client
	refreshTimeout     time.Duration
	maxResponseBytes   int64
	retryMin, retryMax time.Duration
	legacySource       string
	refreshGate        chan struct{}
	health             RefreshHealth
	runGate            chan struct{}
	cacheChecked       bool
	legacyCache        bool
	// onRefresh is called with the client after every successful Refresh.
	// Registered via WithOnRefresh; nil means no-op.
	onRefresh func(*Client)

	mu          sync.RWMutex
	catalog     *Catalog
	lastFetched time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithCacheDir overrides the directory containing source-specific catalog files.
func WithCacheDir(dir string) Option {
	return func(c *Client) { c.cacheDir = dir }
}

// WithCacheTTL sets how long a cached catalog is considered fresh (default: 24h).
func WithCacheTTL(d time.Duration) Option {
	return func(c *Client) { c.ttl = d }
}

// WithHTTPClient replaces the HTTP client used for fetches (useful in tests).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithURL overrides the API endpoint (useful in tests or for alternate sources).
func WithURL(url string) Option {
	return func(c *Client) { c.url = url }
}

// WithOnRefresh registers a callback that is called with the client after every
// successful Refresh (both explicit and from StartRefresher). Use this to push
// catalog data into other packages without polling. The callback may read the
// client but must not recursively call Refresh or Run; it runs under the refresh gate.
func WithOnRefresh(fn func(*Client)) Option {
	return func(c *Client) { c.onRefresh = fn }
}

// New returns a Client with default settings applied before any opts.
func New(opts ...Option) *Client {
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		cacheBase = os.TempDir()
	}

	c := &Client{
		url:              defaultURL,
		cacheDir:         filepath.Join(cacheBase, "go-modelsdev"),
		ttl:              defaultTTL,
		httpClient:       &http.Client{Timeout: 30 * time.Second},
		refreshTimeout:   30 * time.Second,
		maxResponseBytes: 64 << 20,
		retryMin:         time.Second, retryMax: time.Minute,
		refreshGate: make(chan struct{}, 1),
	}
	c.runGate = make(chan struct{}, 1)
	for _, o := range opts {
		o(c)
	}
	if c.ttl <= 0 {
		c.ttl = defaultTTL
	}
	if c.refreshTimeout <= 0 {
		c.refreshTimeout = 30 * time.Second
	}
	if c.maxResponseBytes <= 0 || c.maxResponseBytes > 1<<30 {
		c.maxResponseBytes = 64 << 20
	}
	if c.retryMin <= 0 {
		c.retryMin = time.Second
	}
	if c.retryMax < c.retryMin {
		c.retryMax = c.retryMin
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return c
}

// Get returns the Model identified by providerID and modelID.
// The catalog is loaded from disk on first call if it has not been loaded yet.
func (c *Client) Get(providerID, modelID string) (Model, bool) {
	m, _, ok := c.GetWithFetchedAt(providerID, modelID)
	return m, ok
}

// GetWithFetchedAt reads a model and its snapshot's fetch timestamp atomically.
// Returned metadata must be treated as read-only, like all catalog views.
func (c *Client) GetWithFetchedAt(providerID, modelID string) (Model, time.Time, bool) {
	c.ensureCatalog()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.catalog == nil {
		return Model{}, time.Time{}, false
	}
	p, ok := c.catalog.Providers[providerID]
	if !ok {
		return Model{}, time.Time{}, false
	}
	m, ok := p.Models[modelID]
	return m, c.lastFetched, ok
}

// GetProvider returns the Provider identified by providerID.
func (c *Client) GetProvider(providerID string) (Provider, bool) {
	cat := c.ensureCatalog()
	if cat == nil {
		return Provider{}, false
	}
	p, ok := cat.Providers[providerID]
	return p, ok
}

// List returns a flat slice of all models across all providers, sorted
// by provider ID then model ID for stable output.
func (c *Client) List() []ModelRef {
	cat := c.ensureCatalog()
	if cat == nil {
		return nil
	}

	providerIDs := make([]string, 0, len(cat.Providers))
	for pid := range cat.Providers {
		providerIDs = append(providerIDs, pid)
	}
	sort.Strings(providerIDs)

	var refs []ModelRef
	for _, pid := range providerIDs {
		p := cat.Providers[pid]
		modelIDs := make([]string, 0, len(p.Models))
		for mid := range p.Models {
			modelIDs = append(modelIDs, mid)
		}
		sort.Strings(modelIDs)

		for _, mid := range modelIDs {
			m := p.Models[mid]
			refs = append(refs, ModelRef{
				ProviderID:       pid,
				Description:      m.Description,
				CanonicalModelID: m.CanonicalModelID,
				Type:             m.Type,
				Status:           m.Status,
				Provider:         m.Provider,
				ReasoningOptions: m.ReasoningOptions,
				Interleaved:      m.Interleaved,
				Experimental:     m.Experimental,
				ID:               m.ID,
				Name:             m.Name,
				Family:           m.Family,
				OpenWeights:      m.OpenWeights,
				ReleaseDate:      m.ReleaseDate,
				KnowledgeCutoff:  m.KnowledgeCutoff,
				LastUpdated:      m.LastUpdated,
				Cost:             m.Cost,
				Limit:            m.Limit,
				Modality:         m.Modality,
				Capabilities:     m.Capabilities,
			})
		}
	}
	return refs
}

// ListProviders returns all providers sorted by ID.
func (c *Client) ListProviders() []Provider {
	cat := c.ensureCatalog()
	if cat == nil {
		return nil
	}
	ids := make([]string, 0, len(cat.Providers))
	for id := range cat.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Provider, 0, len(ids))
	for _, id := range ids {
		out = append(out, cat.Providers[id])
	}
	return out
}

// LastFetchedAt returns when the cache was last successfully written.
// Returns the zero time if the cache has never been populated.
func (c *Client) LastFetchedAt() time.Time {
	c.ensureCatalog()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastFetched
}

// Refresh fetches fresh data from the API and updates the on-disk cache.
// It retries up to maxRetries times with exponential backoff.
// If the fetch fails, the existing cache remains untouched. So does a fetch
// that completes after ctx is cancelled: Refresh then returns ctx.Err()
// without writing the cache or calling the [WithOnRefresh] callback.
func (c *Client) Refresh(ctx context.Context) (result error) {
	ctx, cancel := context.WithTimeout(ctx, c.refreshTimeout)
	defer cancel()
	select {
	case c.refreshGate <- struct{}{}:
		defer func() { <-c.refreshGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.ensureCatalog() // Serve last-good data even if the first refresh fails.
	c.beginAttempt()
	defer func() {
		if result != nil {
			c.finishAttempt(result)
		}
	}()
	op := func() (Catalog, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
		if err != nil {
			return Catalog{}, backoff.Permanent(&RefreshError{Code: "request"})
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return Catalog{}, ctx.Err()
			}
			return Catalog{}, &RefreshError{Code: "network"}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err := &RefreshError{Code: "http", HTTPStatus: resp.StatusCode}
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
				return Catalog{}, backoff.Permanent(err)
			}
			return Catalog{}, err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
		if err != nil {
			if ctx.Err() != nil {
				return Catalog{}, ctx.Err()
			}
			return Catalog{}, &RefreshError{Code: "read"}
		}
		if int64(len(data)) > c.maxResponseBytes {
			return Catalog{}, backoff.Permanent(&RefreshError{Code: "response_too_large"})
		}
		var raw map[string]Provider
		if err := json.Unmarshal(data, &raw); err != nil {
			return Catalog{}, backoff.Permanent(&RefreshError{Code: "decode"})
		}
		cat := Catalog{Providers: raw}
		if err := validateCatalog(cat); err != nil {
			return Catalog{}, backoff.Permanent(&RefreshError{Code: "invalid_catalog"})
		}
		return cat, nil
	}
	retry := backoff.NewExponentialBackOff()
	retry.InitialInterval = c.retryMin
	retry.MaxInterval = c.retryMax
	retry.RandomizationFactor = 0
	cat, err := backoff.Retry(ctx, op, backoff.WithBackOff(retry), backoff.WithMaxTries(maxRetries))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fetched := time.Now()
	entry := cacheEntry{Version: cacheVersion, Source: c.sourceID(), FetchedAt: fetched, Data: cat}
	// Readers observe publication and fetched-at together; disk publication is
	// serialized by refreshGate. Unique temporary files also isolate other clients.
	if err := writeCache(ctx, c.cachePath(), entry, c.maxResponseBytes); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return &RefreshError{Code: "cache_write"}
	}
	c.mu.Lock()
	c.catalog, c.lastFetched = &cat, fetched
	c.cacheChecked, c.legacyCache = true, false
	c.health.LastCacheError = ""
	c.mu.Unlock()
	// Publish success before the callback so observers see the new health too.
	c.finishAttempt(nil)
	if c.onRefresh != nil {
		c.onRefresh(c)
	}
	return nil
}

// ensureCatalog returns the in-memory catalog, loading from disk if needed.
// Returns nil if no cache exists and the load fails.
func (c *Client) ensureCatalog() *Catalog {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cacheChecked {
		return c.catalog
	}
	c.cacheChecked = true
	entry, legacy, err := c.loadSourceCache()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			c.health.LastCacheError = "invalid_cache"
		}
		return nil
	}
	c.catalog, c.lastFetched, c.legacyCache = &entry.Data, entry.FetchedAt, legacy
	return c.catalog
}
