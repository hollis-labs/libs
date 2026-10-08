package main

import (
	"path/filepath"
	"testing"
)

func TestPublicEmbeddingLifecycle(t *testing.T) {
	if err := exercise(filepath.Join(t.TempDir(), "embedding.db")); err != nil {
		t.Fatal(err)
	}
}
