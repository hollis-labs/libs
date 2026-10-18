package modelsdev

import (
	"context"
	"path/filepath"
	"time"
)

// SaveCacheForTest writes an unbound schema-1 fixture for legacy migration tests.
func SaveCacheForTest(dir string, cat Catalog) error {
	return writeCache(context.Background(), filepath.Join(dir, "catalog.json"), cacheEntry{Version: 1, FetchedAt: time.Now(), Data: cat}, 64<<20)
}
