package modelsdev_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/libs/llm/core/modelsdev"
	"github.com/stretchr/testify/require"
)

func TestRefreshRejectsBadDataAndKeepsLastGoodSnapshot(t *testing.T) {
	var body atomic.Value
	good, err := json.Marshal(minimalCatalog())
	require.NoError(t, err)
	body.Store(string(good))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body.Load().(string)) }))
	defer server.Close()
	dir := t.TempDir()
	c := modelsdev.New(modelsdev.WithURL(server.URL), modelsdev.WithCacheDir(dir), modelsdev.WithMaxResponseBytes(4096))
	require.NoError(t, c.Refresh(context.Background()))
	initial, stamp, ok := c.GetWithFetchedAt("acme", "fast-1")
	require.True(t, ok)
	for _, raw := range []string{
		`{}`, `null`, `[]`, `{"acme":null}`, `{"acme":{"id":"wrong","models":{}}}`,
		`{"acme":{"id":"acme","models":{"m":{"id":"wrong"}}}}`,
		`{"acme":{"id":"acme","models":{}}}`,
		`{"acme":{"id":"acme","models":{"m":{"id":"m","limit":{"context":-1}}}}}`,
		string(good) + ` {}`, strings.Repeat("x", 4097),
	} {
		body.Store(raw)
		require.Error(t, c.Refresh(context.Background()))
		m, fetched, found := c.GetWithFetchedAt("acme", "fast-1")
		require.True(t, found)
		require.Equal(t, initial, m)
		require.Equal(t, stamp, fetched)
		offline := modelsdev.New(modelsdev.WithURL(server.URL), modelsdev.WithCacheDir(dir))
		require.True(t, stamp.Equal(offline.LastFetchedAt())) // First read hydrates timestamp.
		m, found = offline.Get("acme", "fast-1")
		require.True(t, found)
		require.Equal(t, initial, m)
		require.NotEmpty(t, c.Health().LastError)
	}
	body.Store(string(good))
	require.NoError(t, c.Refresh(context.Background()))
	h := c.Health()
	require.Empty(t, h.LastError)
	require.Zero(t, h.ConsecutiveFailures)
	require.Equal(t, "fresh", h.State)
}

func TestCacheSourceIsolationAndLegacyBinding(t *testing.T) {
	dir := t.TempDir()
	source := serveJSON(t, minimalCatalog())
	defer source.Close()
	c := modelsdev.New(modelsdev.WithURL(source.URL), modelsdev.WithCacheDir(dir))
	require.NoError(t, c.Refresh(context.Background()))
	same := modelsdev.New(modelsdev.WithURL(source.URL), modelsdev.WithCacheDir(dir))
	_, found := same.Get("acme", "fast-1")
	require.True(t, found)
	changed := modelsdev.New(modelsdev.WithURL(source.URL+"?type=other"), modelsdev.WithCacheDir(dir))
	_, found = changed.Get("acme", "fast-1")
	require.False(t, found)
	require.NotEqual(t, c.Health().SourceID, changed.Health().SourceID)
	// Even a legacy file cannot cross a source boundary without explicit ownership.
	require.NoError(t, modelsdev.SaveCacheForTest(dir, modelsdev.Catalog{Providers: minimalCatalog()}))
	unbound := modelsdev.New(modelsdev.WithCacheDir(dir))
	_, found = unbound.Get("acme", "fast-1")
	require.False(t, found)
	wrong := modelsdev.New(modelsdev.WithURL("https://example.com/b"), modelsdev.WithLegacyCacheSource("https://example.com/a"), modelsdev.WithCacheDir(dir))
	_, found = wrong.Get("acme", "fast-1")
	require.False(t, found)
	bound := modelsdev.New(modelsdev.WithURL("https://example.com/a"), modelsdev.WithLegacyCacheSource("https://example.com/a"), modelsdev.WithCacheDir(dir))
	_, found = bound.Get("acme", "fast-1")
	require.True(t, found)
	require.True(t, bound.Health().LegacyCache)
	require.Equal(t, "stale", bound.Health().State)
}

func TestRunBacksOffStaleFailuresAndRecovers(t *testing.T) {
	var failing atomic.Bool
	var failures atomic.Int32
	attempts := make(chan time.Time, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			attempts <- time.Now()
			if failures.Add(1) <= 2 {
				w.WriteHeader(400)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(minimalCatalog())
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := modelsdev.New(modelsdev.WithURL(server.URL), modelsdev.WithCacheDir(t.TempDir()), modelsdev.WithCacheTTL(time.Nanosecond), modelsdev.WithRetryBackoff(30*time.Millisecond, 60*time.Millisecond), modelsdev.WithOnRefresh(func(*modelsdev.Client) {
		if failing.Load() {
			cancel()
		}
	}))
	require.NoError(t, c.Refresh(context.Background()))
	failing.Store(true)
	c.Run(ctx)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	first, second, third := <-attempts, <-attempts, <-attempts
	require.GreaterOrEqual(t, second.Sub(first), 30*time.Millisecond)
	require.GreaterOrEqual(t, third.Sub(second), 60*time.Millisecond)
	h := c.Health()
	require.Zero(t, h.ConsecutiveFailures)
	require.Empty(t, h.LastError)
	require.False(t, h.Running)
	require.False(t, h.Refreshing)
}

func TestFailureHealthIsSafeAndBackoffIsCapped(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(401)
		_, _ = io.WriteString(w, "private-response-value")
	}))
	defer server.Close()
	c := modelsdev.New(modelsdev.WithURL(server.URL+"?credential=private-query-value"), modelsdev.WithCacheDir(t.TempDir()), modelsdev.WithRetryBackoff(time.Second, 4*time.Second))
	for _, delay := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second} {
		err := c.Refresh(context.Background())
		require.Error(t, err)
		require.NotContains(t, err.Error(), "private-")
		h := c.Health()
		require.Equal(t, delay, h.NextAttempt.Sub(h.LastFinished))
		require.Equal(t, 401, h.HTTPStatus)
		require.Equal(t, "http", h.LastError)
		encoded, err := json.Marshal(h)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "private-")
	}
	require.Equal(t, int32(4), hits.Load(), "permanent HTTP failures must not trigger internal retries")
}

func TestRefreshDeadlineAndCanceledWaiter(t *testing.T) {
	entered := make(chan struct{})
	c := modelsdev.New(modelsdev.WithURL("https://example.com/catalog"), modelsdev.WithCacheDir(t.TempDir()), modelsdev.WithRefreshTimeout(100*time.Millisecond), modelsdev.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}))
	result := make(chan error, 1)
	go func() { result <- c.Refresh(context.Background()) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, c.Refresh(ctx), context.Canceled)
	require.ErrorIs(t, <-result, context.DeadlineExceeded)
	h := c.Health()
	require.Equal(t, uint64(1), h.Attempts)
	require.Equal(t, "timeout", h.LastError)
	require.False(t, h.Refreshing)
}

func TestConcurrentRefreshesPublishAndCallbackInOrder(t *testing.T) {
	var active atomic.Int32
	var concurrent atomic.Bool
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if active.Add(1) != 1 {
			concurrent.Store(true)
		}
		defer active.Add(-1)
		n := calls.Add(1)
		payload := minimalCatalog()
		provider := payload["acme"]
		m := provider.Models["fast-1"]
		m.Cost.Input = float64(n)
		provider.Models["fast-1"] = m
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()
	var snapshots []float64
	var timestamps []time.Time
	c := modelsdev.New(modelsdev.WithURL(server.URL), modelsdev.WithCacheDir(t.TempDir()), modelsdev.WithOnRefresh(func(c *modelsdev.Client) {
		m, stamp, found := c.GetWithFetchedAt("acme", "fast-1")
		if !found {
			concurrent.Store(true)
			return
		}
		snapshots = append(snapshots, m.Cost.Input)
		timestamps = append(timestamps, stamp)
	}))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- c.Refresh(context.Background()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.False(t, concurrent.Load())
	for i, n := range snapshots {
		require.Equal(t, float64(i+1), n)
		if i > 0 {
			require.False(t, timestamps[i].Before(timestamps[i-1]))
		}
	}
}

func TestCacheWriteFailurePreservesPublishedData(t *testing.T) {
	server := serveJSON(t, minimalCatalog())
	defer server.Close()
	dir := t.TempDir()
	c := modelsdev.New(modelsdev.WithURL(server.URL), modelsdev.WithCacheDir(dir))
	require.NoError(t, c.Refresh(context.Background()))
	stamp := c.LastFetchedAt()
	// Replace the directory with a regular file to make the next write fail.
	moved := dir + "-saved"
	require.NoError(t, os.Rename(dir, moved))
	defer os.RemoveAll(moved)
	require.NoError(t, os.WriteFile(dir, []byte("unwritable cache directory"), 0600))
	err := c.Refresh(context.Background())
	require.Error(t, err)
	var failure *modelsdev.RefreshError
	require.True(t, errors.As(err, &failure))
	require.Equal(t, "cache_write", failure.Code)
	require.Equal(t, stamp, c.LastFetchedAt())
	_, found := c.Get("acme", "fast-1")
	require.True(t, found)
	// The previous snapshot remains usable on disk too.
	offline := modelsdev.New(modelsdev.WithURL(server.URL), modelsdev.WithCacheDir(moved))
	require.True(t, stamp.Equal(offline.LastFetchedAt()))
	entries, err := os.ReadDir(moved)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NotEqual(t, ".tmp", filepath.Ext(entry.Name()))
	}
}

func TestCacheRejectsWrongSourceAndFutureSchema(t *testing.T) {
	server := serveJSON(t, minimalCatalog())
	defer server.Close()
	dir := t.TempDir()
	c := modelsdev.New(modelsdev.WithURL(server.URL), modelsdev.WithCacheDir(dir))
	require.NoError(t, c.Refresh(context.Background()))
	filename := filepath.Join(dir, "catalog-v2-"+c.Health().SourceID+".json")
	original, err := os.ReadFile(filename)
	require.NoError(t, err)
	for _, change := range []func(map[string]any){
		func(v map[string]any) { v["source_id"] = "another-source" },
		func(v map[string]any) { v["schema_version"] = 99 },
		func(v map[string]any) { v["fetched_at"] = time.Now().Add(24 * time.Hour).Format(time.RFC3339) },
	} {
		var data map[string]any
		require.NoError(t, json.Unmarshal(original, &data))
		change(data)
		encoded, err := json.Marshal(data)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filename, encoded, 0600))
		offline := modelsdev.New(modelsdev.WithURL(server.URL), modelsdev.WithCacheDir(dir))
		_, found := offline.Get("acme", "fast-1")
		require.False(t, found)
		require.Equal(t, "invalid_cache", offline.Health().LastCacheError)
		require.Equal(t, "empty", offline.Health().State)
	}
}
