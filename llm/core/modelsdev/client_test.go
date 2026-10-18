package modelsdev_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"

	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/llm/core/modelsdev"
	"github.com/stretchr/testify/require"
)

// minimalCatalog returns a small but valid API payload for use in tests.
func minimalCatalog() map[string]modelsdev.Provider {
	return map[string]modelsdev.Provider{
		"acme": {
			ID:   "acme",
			Name: "Acme AI",
			Env:  []string{"ACME_API_KEY"},
			Models: map[string]modelsdev.Model{
				"fast-1": {
					ID:     "fast-1",
					Name:   "Fast Model 1",
					Family: "fast",
					Cost:   modelsdev.Pricing{Input: 1.0, Output: 2.0},
					Limit:  modelsdev.Limits{ContextWindow: 128000, MaxOutputTokens: 4096},
					Capabilities: modelsdev.Capabilities{
						ToolCall:    true,
						Temperature: true,
					},
				},
				"slow-2": {
					ID:     "slow-2",
					Name:   "Slow Model 2",
					Family: "slow",
				},
			},
		},
	}
}

func serveJSON(t *testing.T, payload any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
}

func serve500(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
}

func newTestClient(t *testing.T, srv *httptest.Server) *modelsdev.Client {
	t.Helper()
	return modelsdev.New(
		modelsdev.WithURL(srv.URL),
		modelsdev.WithHTTPClient(srv.Client()),
		modelsdev.WithCacheDir(t.TempDir()),
		modelsdev.WithCacheTTL(24*time.Hour),
		modelsdev.WithRetryBackoff(time.Millisecond, time.Millisecond),
	)
}

func TestGet(t *testing.T) {
	srv := serveJSON(t, minimalCatalog())
	defer srv.Close()

	c := newTestClient(t, srv)
	require.NoError(t, c.Refresh(context.Background()))

	m, ok := c.Get("acme", "fast-1")
	require.True(t, ok)
	require.Equal(t, "Fast Model 1", m.Name)
	require.Equal(t, 1.0, m.Cost.Input)
	require.True(t, m.Capabilities.ToolCall)
}

func TestGet_Miss(t *testing.T) {
	srv := serveJSON(t, minimalCatalog())
	defer srv.Close()

	c := newTestClient(t, srv)
	require.NoError(t, c.Refresh(context.Background()))

	_, ok := c.Get("acme", "nonexistent")
	require.False(t, ok)

	_, ok = c.Get("unknown-provider", "fast-1")
	require.False(t, ok)
}

func TestList(t *testing.T) {
	srv := serveJSON(t, minimalCatalog())
	defer srv.Close()

	c := newTestClient(t, srv)
	require.NoError(t, c.Refresh(context.Background()))

	refs := c.List()
	require.Len(t, refs, 2)

	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = r.ID
		require.Equal(t, "acme", r.ProviderID)
	}
	require.Contains(t, ids, "fast-1")
	require.Contains(t, ids, "slow-2")
}

func TestRefresh_Fallback(t *testing.T) {
	// Seed a good cache first.
	goodSrv := serveJSON(t, minimalCatalog())
	defer goodSrv.Close()

	dir := t.TempDir()
	c := modelsdev.New(
		modelsdev.WithURL(goodSrv.URL),
		modelsdev.WithHTTPClient(goodSrv.Client()),
		modelsdev.WithCacheDir(dir),
	)
	require.NoError(t, c.Refresh(context.Background()))

	// Now point at a server that returns 500.
	badSrv := serve500(t)
	defer badSrv.Close()

	c2 := modelsdev.New(
		modelsdev.WithURL(goodSrv.URL),
		modelsdev.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("unavailable")), Header: make(http.Header)}, nil
		})}),
		modelsdev.WithCacheDir(dir), // same cache dir — old cache must survive
		modelsdev.WithCacheTTL(24*time.Hour),
		modelsdev.WithRetryBackoff(time.Millisecond, time.Millisecond),
	)

	err := c2.Refresh(context.Background())
	require.Error(t, err)

	// A fresh client reading the same cache dir should still see the old catalog.
	c3 := modelsdev.New(
		modelsdev.WithURL(goodSrv.URL),
		modelsdev.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("unavailable")), Header: make(http.Header)}, nil
		})}),
		modelsdev.WithCacheDir(dir),
	)
	m, ok := c3.Get("acme", "fast-1")
	require.True(t, ok, "old cache must be intact after failed refresh")
	require.Equal(t, "Fast Model 1", m.Name)
}

func TestRefresh_AtomicWrite(t *testing.T) {
	srv := serveJSON(t, minimalCatalog())
	defer srv.Close()

	dir := t.TempDir()
	c := modelsdev.New(
		modelsdev.WithURL(srv.URL),
		modelsdev.WithHTTPClient(srv.Client()),
		modelsdev.WithCacheDir(dir),
	)
	require.NoError(t, c.Refresh(context.Background()))

	// A successful atomic publish leaves no temporary file behind.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		require.False(t, strings.HasSuffix(entry.Name(), ".tmp"))
	}

}

func TestLastFetchedAt(t *testing.T) {
	srv := serveJSON(t, minimalCatalog())
	defer srv.Close()

	c := newTestClient(t, srv)
	require.True(t, c.LastFetchedAt().IsZero(), "should be zero before any refresh")

	before := time.Now()
	require.NoError(t, c.Refresh(context.Background()))
	after := time.Now()

	ts := c.LastFetchedAt()
	require.False(t, ts.IsZero())
	require.True(t, !ts.Before(before), "LastFetchedAt should be >= before")
	require.True(t, !ts.After(after), "LastFetchedAt should be <= after")
}
