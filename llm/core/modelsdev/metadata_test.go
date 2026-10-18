package modelsdev_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/libs/llm/core/modelsdev"
	"github.com/stretchr/testify/require"
)

// Synthetic rates avoid making this frozen schema fixture a pricing authority.
// Shape: anomalyco/models.dev packages/core/src/schema.ts, reviewed 2026-10-18.
func TestCurrentMetadataSurvivesRefreshListAndRestart(t *testing.T) {
	fixture, err := os.ReadFile("testdata/catalog-current.json")
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fixture) }))
	defer server.Close()
	dir := t.TempDir()
	live := modelsdev.New(modelsdev.WithCacheDir(dir), modelsdev.WithURL(server.URL))
	require.NoError(t, live.Refresh(context.Background()))
	offline := modelsdev.New(modelsdev.WithCacheDir(dir))
	for _, client := range []*modelsdev.Client{live, offline} {
		m, ok := client.Get("relay", "offering")
		require.True(t, ok)
		require.Equal(t, "lab/underlying", m.CanonicalModelID)
		require.Equal(t, "decision", m.Type)
		require.Equal(t, "beta", m.Status)
		require.Equal(t, 160000, *m.Limit.MaxInputTokens)
		require.Equal(t, "responses", m.Provider.Shape)
		require.JSONEq(t, `{"value":1}`, string(m.Provider.Body["nested"]))
		require.Equal(t, "value", m.Provider.Headers["X-Example"])
		require.Equal(t, "reasoning_details", m.Interleaved.Field)
		require.Nil(t, m.ReasoningOptions[1].Values[0])
		require.Equal(t, "high", *m.ReasoningOptions[1].Values[2])
		require.Equal(t, -1, *m.ReasoningOptions[2].Min)
		require.Equal(t, 8192, *m.ReasoningOptions[2].Max)
		require.Equal(t, 3.0, m.Experimental.Modes["fast"].Cost.Input)
		require.Equal(t, "fast", m.Experimental.Modes["fast"].Provider.Headers["X-Mode"])
		require.False(t, m.Capabilities.ToolCall)
		require.False(t, m.Capabilities.Unknown.ToolCall)
		require.False(t, m.Capabilities.StructuredOutput)
		require.False(t, m.Capabilities.Unknown.StructuredOutput)
		require.True(t, m.Capabilities.Unknown.Temperature)
		require.False(t, m.Cost.UnknownRates.Input) // Explicitly free, never absent.
		require.True(t, m.Cost.UnknownRates.CacheWrite)
		require.Equal(t, 10.0, *m.Cost.InputAudio)
		require.Equal(t, 0.0, *m.Cost.OutputAudio)
		require.False(t, client.LastFetchedAt().IsZero())
		refs := client.List()
		var ref modelsdev.ModelRef
		for _, r := range refs {
			if r.ProviderID == "relay" && r.ID == "offering" {
				ref = r
			}
		}
		require.Equal(t, m.CanonicalModelID, ref.CanonicalModelID)
		require.Equal(t, m.Description, ref.Description)
		require.Equal(t, m.Type, ref.Type)
		require.Equal(t, m.Status, ref.Status)
		require.Equal(t, m.Provider, ref.Provider)
		require.Equal(t, m.ReasoningOptions, ref.ReasoningOptions)
		require.Equal(t, m.Interleaved, ref.Interleaved)
		require.Equal(t, m.Experimental, ref.Experimental)
		require.Equal(t, m.Cost, ref.Cost)
		require.Equal(t, m.Capabilities, ref.Capabilities)
		original, _ := client.Get("lab", "underlying")
		require.Equal(t, 9.0, original.Cost.Input) // Same canonical identity, separate prices.
		tiered, _ := client.Get("relay", "tiered")
		require.NotNil(t, tiered.Interleaved)
		require.Empty(t, tiered.Interleaved.Field)
		require.True(t, tiered.Cost.HasContextTiers())
		require.Equal(t, int64(200000), tiered.Cost.Tiers[0].Tier.Size)
		require.Equal(t, 2.0, tiered.Cost.ContextOver200K.Input)
		require.False(t, tiered.Cost.Tiers[0].Pricing.UnknownRates.CacheRead)
		unknown, _ := client.Get("relay", "unknown")
		require.Equal(t, modelsdev.UnknownPricing(), unknown.Cost)
		require.True(t, unknown.Capabilities.Unknown.ToolCall)
	}
	require.Equal(t, live.List(), offline.List())
}

func TestPricingAbsenceAndFreeRoundTrip(t *testing.T) {
	for _, raw := range []string{`{}`, `{"cost":null}`, `{"cost":{}}`, `{"cost":{"input":null}}`} {
		var m modelsdev.Model
		require.NoError(t, json.Unmarshal([]byte(raw), &m))
		require.Equal(t, modelsdev.UnknownPricing(), m.Cost)
		data, err := json.Marshal(m)
		require.NoError(t, err)
		var again modelsdev.Model
		require.NoError(t, json.Unmarshal(data, &again))
		require.Equal(t, m, again)
	}
	var p modelsdev.Pricing
	require.NoError(t, json.Unmarshal([]byte(`{"input":0,"cache_read":0,"output":2}`), &p))
	require.False(t, p.UnknownRates.Input)
	require.False(t, p.UnknownRates.CacheRead)
	require.True(t, p.UnknownRates.Reasoning)
	data, err := json.Marshal(p)
	require.NoError(t, err)
	require.JSONEq(t, `{"input":0,"output":2,"cache_read":0}`, string(data))
	require.NoError(t, json.Unmarshal([]byte(`{}`), &p)) // Decoding into a reused value clears prior rates.
	require.Equal(t, modelsdev.UnknownPricing(), p)
	for _, invalid := range []string{`{"input":-1}`, `{"output":1e999}`, `{"input_audio":-1}`, `{"tiers":[{"tier":{"size":-1},"input":1}]}`} {
		require.Error(t, json.Unmarshal([]byte(invalid), &p))
	}
}

func TestLegacyCacheKeepsMetadataWithoutInventingFreeRates(t *testing.T) {
	dir := t.TempDir()
	fetched := time.Now().UTC().Truncate(time.Second)
	old := `{"fetched_at":"` + fetched.Format(time.RFC3339) + `","data":{"Providers":{"p":{"id":"p","models":{"m":{"id":"m","name":"Retained offline","tool_call":false,"reasoning":true,"cost":{"input":0,"output":2}}}}}}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(old), 0600))
	c := modelsdev.New(modelsdev.WithCacheDir(dir))
	m, ok := c.Get("p", "m")
	require.True(t, ok)
	require.Equal(t, "Retained offline", m.Name)
	require.True(t, m.Cost.UnknownRates.Input)
	require.False(t, m.Cost.UnknownRates.Output)
	require.True(t, m.Capabilities.Unknown.ToolCall)
	require.False(t, m.Capabilities.Unknown.Reasoning)
	require.Equal(t, fetched, c.LastFetchedAt())

	// Even a recently fetched legacy cache is refreshed to recover availability.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"p":{"id":"p","models":{"m":{"id":"m","cost":{"input":0,"output":2}}}}}`))
	}))
	defer server.Close()
	upgraded := modelsdev.New(modelsdev.WithCacheDir(dir), modelsdev.WithURL(server.URL), modelsdev.WithOnRefresh(func(*modelsdev.Client) { cancel() }))
	upgraded.Run(ctx)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	fresh := modelsdev.New(modelsdev.WithCacheDir(dir))
	m, ok = fresh.Get("p", "m")
	require.True(t, ok)
	require.False(t, m.Cost.UnknownRates.Input)
}

func TestReasoningEmptyControlsRemainExplicit(t *testing.T) {
	var m modelsdev.Model
	require.NoError(t, json.Unmarshal([]byte(`{"reasoning_options":[]}`), &m))
	data, err := json.Marshal(m)
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &wire))
	require.JSONEq(t, `[]`, string(wire["reasoning_options"]))
	require.NoError(t, json.Unmarshal([]byte(`{}`), &m))
	data, err = json.Marshal(m)
	require.NoError(t, err)
	wire = nil
	require.NoError(t, json.Unmarshal(data, &wire))
	_, present := wire["reasoning_options"]
	require.False(t, present)
}

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDefaultRequestIncludesSpecializedModels(t *testing.T) {
	fixture, err := os.ReadFile("testdata/catalog-current.json")
	require.NoError(t, err)
	c := modelsdev.New(modelsdev.WithCacheDir(t.TempDir()), modelsdev.WithHTTPClient(&http.Client{
		Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
			require.Equal(t, "all", r.URL.Query().Get("type"))
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(fixture)), Header: make(http.Header)}, nil
		}),
	}))
	require.NoError(t, c.Refresh(context.Background()))
	m, found := c.Get("relay", "offering")
	require.True(t, found)
	require.Equal(t, "decision", m.Type)
}
