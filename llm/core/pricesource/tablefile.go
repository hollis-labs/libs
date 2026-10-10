package pricesource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// tableFile is the JSON form of a Table: what WriteJSON writes and
// ParseTable reads.
type tableFile struct {
	Name    string    `json:"name"`
	AsOf    time.Time `json:"as_of,omitzero"`
	Entries []Entry   `json:"entries"`
}

// ParseTable reads a table in the JSON form WriteJSON writes:
//
//	{"name": "...", "as_of": "RFC 3339", "entries": [{"provider": "...",
//	 "model": "...", "input_per_million": 3, ...}]}
//
// A rate that is absent is unknown; a rate of 0 is free. The entries are
// validated as NewTable does.
func ParseTable(r io.Reader) (*Table, error) {
	var f tableFile
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("pricesource: parse table: %w", err)
	}
	return NewTable(f.Name, f.AsOf, f.Entries...)
}

// WriteJSON writes the table in the form ParseTable reads, entries sorted by
// provider then model, so the same table always writes the same bytes.
func (t *Table) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(tableFile{Name: t.name, AsOf: t.asOf, Entries: t.Entries()})
}

// LoadFile reads a table file written by SaveFile.
func LoadFile(path string) (*Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("pricesource: %w", err)
	}
	defer f.Close()
	return ParseTable(f)
}

// SaveFile writes t to path atomically: a temporary file in the same
// directory, then a rename, so a reader never sees a half-written table and
// a failed write leaves the previous file in place. The directory is created
// if needed.
func SaveFile(path string, t *Table) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("pricesource: create dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("pricesource: create temp file: %w", err)
	}
	if err := t.WriteJSON(tmp); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("pricesource: write table: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("pricesource: close temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("pricesource: rename: %w", err)
	}
	return nil
}

// FileCache keeps a table available offline. Refresh fetches a fresh table
// through the caller's fetch function and saves it; when fetching fails it
// falls back to the last saved copy. The package itself does no network I/O.
type FileCache struct {
	// Path is the cache file. Required.
	Path string
}

// Load returns the saved table.
func (c FileCache) Load() (*Table, error) { return LoadFile(c.Path) }

// Refresh calls fetch. On success it saves the result and returns it; if
// saving fails it still returns the fresh table, with the save error. When
// fetch fails it returns the saved table and fetch's error, so a caller can
// keep pricing offline and still log why the refresh failed. When neither
// works it returns a nil table and both errors.
func (c FileCache) Refresh(ctx context.Context, fetch func(context.Context) (*Table, error)) (*Table, error) {
	fresh, ferr := fetch(ctx)
	if ferr == nil && fresh != nil {
		if err := SaveFile(c.Path, fresh); err != nil {
			return fresh, err
		}
		return fresh, nil
	}
	if ferr == nil {
		ferr = errors.New("pricesource: fetch returned no table")
	}
	saved, lerr := c.Load()
	if lerr != nil {
		return nil, errors.Join(ferr, lerr)
	}
	return saved, ferr
}

// liteLLMEntry is the subset of a LiteLLM-style price entry this package
// reads. Rates are USD per token.
type liteLLMEntry struct {
	Provider       string   `json:"litellm_provider"`
	Input          *float64 `json:"input_cost_per_token"`
	Output         *float64 `json:"output_cost_per_token"`
	CacheRead      *float64 `json:"cache_read_input_token_cost"`
	CacheWrite     *float64 `json:"cache_creation_input_token_cost"`
	ReasoningToken *float64 `json:"output_cost_per_reasoning_token"`
}

// ParseLiteLLM reads a LiteLLM-style price table: a JSON object keyed by
// model name whose values carry per-token USD rates (input_cost_per_token,
// output_cost_per_token, cache_read_input_token_cost,
// cache_creation_input_token_cost, output_cost_per_reasoning_token) and a
// litellm_provider. Rates are converted to per million tokens. A rate the
// entry does not carry is unknown, including the reasoning rate, which most
// entries leave out; such usage prices as partial, never as free.
//
// The provider is litellm_provider, and a "<provider>/" prefix on the key is
// removed from the model name. An entry with no provider is stored under
// AnyProvider. The "sample_spec" key, entries with no rate at all, and
// entries that do not decode are skipped; skipped returns their keys, sorted.
func ParseLiteLLM(r io.Reader, name string, asOf time.Time) (t *Table, skipped []string, err error) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("pricesource: parse litellm table: %w", err)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var entries []Entry
	seen := make(map[string]bool)
	for _, k := range keys {
		if k == "sample_spec" {
			skipped = append(skipped, k)
			continue
		}
		var le liteLLMEntry
		if err := json.Unmarshal(raw[k], &le); err != nil {
			skipped = append(skipped, k)
			continue
		}
		if le.Input == nil && le.Output == nil && le.CacheRead == nil && le.CacheWrite == nil && le.ReasoningToken == nil {
			skipped = append(skipped, k)
			continue
		}
		provider, model := le.Provider, k
		if provider == "" {
			provider = AnyProvider
		} else {
			model = strings.TrimPrefix(k, provider+"/")
		}
		e := Entry{
			Provider:   provider,
			Model:      model,
			Input:      perMillion(le.Input),
			Output:     perMillion(le.Output),
			CacheWrite: perMillion(le.CacheWrite),
			CacheRead:  perMillion(le.CacheRead),
			Reasoning:  perMillion(le.ReasoningToken),
		}
		if e.validate() != nil || seen[e.key()] {
			skipped = append(skipped, k)
			continue
		}
		seen[e.key()] = true
		entries = append(entries, e)
	}
	t, err = NewTable(name, asOf, entries...)
	if err != nil {
		return nil, skipped, err
	}
	return t, skipped, nil
}

func perMillion(perToken *float64) *float64 {
	if perToken == nil {
		return nil
	}
	return Rate(*perToken * 1_000_000)
}
