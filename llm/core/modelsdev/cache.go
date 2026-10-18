package modelsdev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const cacheVersion = 2

type cacheEntry struct {
	Version   int       `json:"schema_version"`
	Source    string    `json:"source_id,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
	Data      Catalog   `json:"data"`
}

func (c *Client) sourceID() string {
	sum := sha256.Sum256([]byte(c.url))
	return hex.EncodeToString(sum[:])
}
func (c *Client) cachePath() string {
	return filepath.Join(c.cacheDir, "catalog-v2-"+c.sourceID()+".json")
}

func (c *Client) loadSourceCache() (*cacheEntry, bool, error) {
	entry, err := readCache(c.cachePath(), c.maxResponseBytes)
	if err == nil {
		if entry.Version != cacheVersion || entry.Source != c.sourceID() {
			return nil, false, fmt.Errorf("cache source mismatch")
		}
		return entry, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) || c.legacySource == "" || c.legacySource != c.url {
		return nil, false, err
	}
	entry, err = readCache(filepath.Join(c.cacheDir, "catalog.json"), c.maxResponseBytes)
	if err != nil {
		return nil, false, err
	}
	if entry.Version >= cacheVersion {
		return nil, false, fmt.Errorf("unbound cache version")
	}
	return entry, true, nil
}

func readCache(path string, maxBytes int64) (*cacheEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("cache too large")
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, err
	}
	if entry.Version < 0 || entry.Version > cacheVersion {
		return nil, fmt.Errorf("unsupported cache schema")
	}
	if entry.FetchedAt.IsZero() || entry.FetchedAt.After(time.Now().Add(time.Minute)) {
		return nil, fmt.Errorf("invalid cache timestamp")
	}
	if err := validateCatalog(entry.Data); err != nil {
		return nil, err
	}
	if entry.Version == 0 {
		// Legacy encoders wrote absent input/output as zero and false flags as
		// known. Keep offline metadata, but do not invent certainty from it.
		for pid, provider := range entry.Data.Providers {
			for mid, model := range provider.Models {
				p := &model.Cost
				p.UnknownRates.Input = p.UnknownRates.Input || p.Input == 0
				p.UnknownRates.Output = p.UnknownRates.Output || p.Output == 0
				p.UnknownRates.CacheWrite = p.UnknownRates.CacheWrite || p.CacheWrite == 0
				p.UnknownRates.CacheRead = p.UnknownRates.CacheRead || p.CacheRead == 0
				p.UnknownRates.Reasoning = p.UnknownRates.Reasoning || p.Reasoning == 0
				c := &model.Capabilities
				c.Unknown = UnknownCapabilities{
					ToolCall: !c.ToolCall, Reasoning: !c.Reasoning,
					Attachment: !c.Attachment, Temperature: !c.Temperature,
					StructuredOutput: !c.StructuredOutput,
				}
				provider.Models[mid] = model
			}
			entry.Data.Providers[pid] = provider
		}
	}
	return &entry, nil
}

// writeCache uses a unique temporary file; cancellation before rename preserves
// the old snapshot. Rename is the publication point, after which memory must follow.
func writeCache(ctx context.Context, path string, entry cacheEntry, maxBytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("cache too large")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".catalog-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func validateCatalog(cat Catalog) error {
	models := 0
	if len(cat.Providers) == 0 {
		return fmt.Errorf("empty catalog")
	}
	for id, p := range cat.Providers {
		if id == "" || p.ID != id {
			return fmt.Errorf("invalid provider identity")
		}
		for mid, m := range p.Models {
			if mid == "" || m.ID != mid {
				return fmt.Errorf("invalid model identity")
			}
			if m.Limit.ContextWindow < 0 || m.Limit.MaxOutputTokens < 0 || (m.Limit.MaxInputTokens != nil && *m.Limit.MaxInputTokens < 0) {
				return fmt.Errorf("invalid model limit")
			}
			models++
		}
	}
	if models == 0 {
		return fmt.Errorf("empty model catalog")
	}
	return nil
}
