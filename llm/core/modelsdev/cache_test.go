package modelsdev_test

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/llm/core/modelsdev"
	"github.com/stretchr/testify/require"
)

func sampleCatalog() modelsdev.Catalog {
	return modelsdev.Catalog{
		Providers: map[string]modelsdev.Provider{
			"example": {
				ID:   "example",
				Name: "Example Provider",
				Models: map[string]modelsdev.Model{
					"m1": {ID: "m1", Name: "Model One"},
				},
			},
		},
	}
}

func TestSaveCache_NoTmpOnSuccess(t *testing.T) {
	dir := t.TempDir()
	err := modelsdev.SaveCacheForTest(dir, sampleCatalog())
	require.NoError(t, err)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		require.False(t, strings.HasSuffix(entry.Name(), ".tmp"))
	}

	main := filepath.Join(dir, "catalog.json")
	_, statErr := os.Stat(main)
	require.NoError(t, statErr, "catalog.json must exist after successful save")
}

func TestSaveCache_OldPreservedOnFailure(t *testing.T) {
	dir := t.TempDir()

	// Write a known-good initial cache.
	initial := sampleCatalog()
	require.NoError(t, modelsdev.SaveCacheForTest(dir, initial))

	// An unencodable update must preserve the previous file and clean its temp.
	bad := sampleCatalog()
	provider := bad.Providers["example"]
	model := provider.Models["m1"]
	model.Cost.Input = math.NaN()
	provider.Models["m1"] = model
	err := modelsdev.SaveCacheForTest(dir, bad)
	require.Error(t, err)

	// The original catalog.json must still be intact and readable.
	f, err := os.Open(filepath.Join(dir, "catalog.json"))
	require.NoError(t, err)
	defer f.Close()

	type cacheEntry struct {
		Data modelsdev.Catalog `json:"data"`
	}
	var entry cacheEntry
	require.NoError(t, json.NewDecoder(f).Decode(&entry))
	require.Contains(t, entry.Data.Providers, "example", "original cache must be preserved after write failure")
}
