package embed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/core/util/classify"
)

var _ classify.Embedder = (*Client)(nil)

// fixtureKey is made up; built at run time so scanners do not read it as a credential.
var fixtureKey = strings.Repeat("k", 12) + "-fixture-" + strings.Repeat("7", 12)

func gateway(t *testing.T, dim int, seen *[]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ai/embeddings" || r.Header.Get("Authorization") != "Bearer "+fixtureKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad key ` + fixtureKey + `"}}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(b, &req)
		*seen = append(*seen, req)
		in := req["request"].(map[string]any)["embedding_input"].([]any)
		type vec struct {
			Index  int       `json:"index"`
			Vector []float32 `json:"vector"`
		}
		var out []vec
		for i := len(in) - 1; i >= 0; i-- { // out of order on purpose
			v := make([]float32, dim)
			v[0] = float32(len(in[i].(string)))
			out = append(out, vec{i, v})
		}
		raw, _ := json.Marshal(map[string]any{"response": map[string]any{"model": "m", "embeddings": out}})
		_, _ = w.Write(raw)
	}))
}

func TestEmbedBatchesAndKeepsOrder(t *testing.T) {
	var seen []map[string]any
	srv := gateway(t, 3, &seen)
	defer srv.Close()
	c := NewClient(srv.URL, fixtureKey, "test", "text-embedding-3-small")
	c.MaxBatch = 2
	vecs, err := c.Embed(context.Background(), []string{"a", "bb", "ccc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 || vecs[0][0] != 1 || vecs[1][0] != 2 || vecs[2][0] != 3 {
		t.Fatalf("vectors %v", vecs)
	}
	if len(seen) != 2 {
		t.Fatalf("%d requests for 3 texts in batches of 2", len(seen))
	}
	req := seen[0]["request"].(map[string]any)
	if req["operation"] != "embedding" || req["model_hint"] != "text-embedding-3-small" || req["caller_id"] != "test" {
		t.Fatalf("request %v", req)
	}
	if c.Name() != "embed-http:text-embedding-3-small" {
		t.Fatalf("name %q", c.Name())
	}
}

func TestEmbedErrorsDoNotLeakTheToken(t *testing.T) {
	var seen []map[string]any
	srv := gateway(t, 3, &seen)
	defer srv.Close()
	c := NewClient(srv.URL, "wrong-"+fixtureKey, "test", "")
	_, err := c.Embed(context.Background(), []string{"secret text"})
	if err == nil || strings.Contains(err.Error(), "wrong-"+fixtureKey) || strings.Contains(err.Error(), "secret text") {
		t.Fatalf("error %v", err)
	}
}

func TestEmbedRefusesADimensionChange(t *testing.T) {
	var seen []map[string]any
	a := gateway(t, 3, &seen)
	defer a.Close()
	b := gateway(t, 4, &seen)
	defer b.Close()
	c := NewClient(a.URL, fixtureKey, "test", "")
	if _, err := c.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	c.BaseURL = b.URL
	if _, err := c.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("a dimension change was accepted")
	}
}

func TestCentroidRunsOnTheGateway(t *testing.T) {
	var seen []map[string]any
	srv := gateway(t, 2, &seen)
	defer srv.Close()
	emb := NewClient(srv.URL, fixtureKey, "test", "m")
	cj := classify.NewCentroid("centroid", "size", emb, classify.CentroidOptions{})
	err := cj.Fit(context.Background(), []classify.Example{
		{Text: "a", Labels: map[string]string{"size": "short"}},
		{Text: "a much longer text", Labels: map[string]string{"size": "long"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v := cj.Version(); !strings.HasPrefix(v, "embed-http:m") {
		t.Fatalf("version %q", v)
	}
}

func TestNewClientWithoutURLIsNil(t *testing.T) {
	if NewClient("", "", "", "") != nil {
		t.Fatal("client without a URL")
	}
}

func TestOpenAIDialect(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"object":"list","model":"qwen3-embedding-0.6b","data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()
	c := NewOpenAIClient(srv.URL, fixtureKey, "embed-local")
	vecs, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/embeddings" || got["model"] != "embed-local" || len(got["input"].([]any)) != 2 {
		t.Fatalf("request %s %v", path, got)
	}
	if vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("vectors %v", vecs)
	}
	c.Model = ""
	if _, err := c.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatal("no model accepted")
	}
}
