package memstore_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	toolresult "github.com/hollis-labs/go-toolresult"
	"github.com/hollis-labs/go-toolresult/memstore"
	"github.com/hollis-labs/go-toolresult/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) toolresult.Store { return memstore.New() })
}

// TestConcurrentCacheUse drives Present, Read, Search and Purge from many
// goroutines; the value is in running it under -race.
func TestConcurrentCacheUse(t *testing.T) {
	ctx := context.Background()
	cache := toolresult.New(memstore.New(), toolresult.Config{HardCapBytes: 1 << 20, DefaultBudget: 256})
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scope := fmt.Sprintf("s%d", i%4)
			body := strings.Repeat(fmt.Sprintf("row %d needle\n", i), 100)
			view, err := cache.Present(ctx, scope, toolresult.Meta{Tool: "t"}, body, 0)
			if err != nil || !view.Cached {
				t.Errorf("Present: %+v, %v", view, err)
				return
			}
			if page, err := cache.Read(ctx, scope, view.CacheID, "", 0, 0, 0); err != nil || page.TotalBytes != len(body) {
				t.Errorf("Read: %+v, %v", page, err)
			}
			if _, err := cache.Search(ctx, scope, view.CacheID, "", "needle", 0, 5, 0); err != nil {
				t.Errorf("Search: %v", err)
			}
			if _, err := cache.Purge(ctx); err != nil {
				t.Errorf("Purge: %v", err)
			}
		}()
	}
	wg.Wait()
}
