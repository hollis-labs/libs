package store

import (
	"fmt"
	"strings"
	"testing"
)

func TestRebindCacheIsBounded(t *testing.T) {
	c := newRebindCache()
	for i := 0; i < 3*rebindCacheMax; i++ {
		c.store(fmt.Sprintf("SELECT %d", i), "x")
	}
	c.store(strings.Repeat("y", rebindKeyMax+1), "x")
	if len(c.m) > rebindCacheMax {
		t.Fatalf("cache holds %d entries, cap %d", len(c.m), rebindCacheMax)
	}
	if _, ok := c.load(strings.Repeat("y", rebindKeyMax+1)); ok {
		t.Fatal("oversized statement was cached")
	}
}
