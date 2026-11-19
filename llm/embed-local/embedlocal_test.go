package embedlocal

import (
	"context"
	"math"
	"os"
	"testing"

	"github.com/hollis-labs/libs/core/util/classify"
)

var _ classify.Embedder = (*Embedder)(nil)

func TestNewRefusesBadOptions(t *testing.T) {
	ctx := context.Background()
	if _, err := New(ctx, Options{}); err == nil {
		t.Error("no model path accepted")
	}
	if _, err := New(ctx, Options{ModelPath: "/nonexistent", Backend: "tpu"}); err == nil {
		t.Error("unknown backend accepted")
	}
	if _, err := New(ctx, Options{ModelPath: "/nonexistent"}); err == nil {
		t.Error("a missing model loaded")
	}
}

// modelPath is a downloaded model, named by EMBED_LOCAL_MODEL; without it the
// model tests are skipped (CI does not download models).
func modelPath(t *testing.T) string {
	t.Helper()
	p := os.Getenv("EMBED_LOCAL_MODEL")
	if p == "" {
		t.Skip("EMBED_LOCAL_MODEL is not set: no local model to test with")
	}
	return p
}

func cosine(a, b []float32) float64 {
	var d, na, nb float64
	for i := range a {
		d += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	return d / math.Sqrt(na*nb)
}

func TestEmbedsWithALocalModel(t *testing.T) {
	e, err := New(context.Background(), Options{ModelPath: modelPath(t), MaxBatch: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	texts := []string{"fix the failing build", "the build is broken, repair it", "what a lovely sunny day"}
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 || len(vecs[0]) == 0 {
		t.Fatalf("vectors %d", len(vecs))
	}
	var n float64
	for _, x := range vecs[0] {
		n += float64(x) * float64(x)
	}
	if math.Abs(n-1) > 1e-3 {
		t.Errorf("not normalised: |v|² = %v", n)
	}
	near, far := cosine(vecs[0], vecs[1]), cosine(vecs[0], vecs[2])
	if near <= far {
		t.Errorf("similar texts are not nearer: %.3f vs %.3f", near, far)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(context.Background(), texts); err == nil {
		t.Error("embedded after Close")
	}
}

// A sentence-embedding model groups texts by topic more than by speech act ("how come the
// tests broke?" sits nearer "fix the failing test" than any question), so a centroid
// judge suits a topic-like facet such as a slot.
func TestCentroidOnALocalModel(t *testing.T) {
	e, err := New(context.Background(), Options{ModelPath: modelPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	c := classify.NewCentroid("centroid:topic", "topic", e, classify.CentroidOptions{Phase: classify.Keystroke})
	err = c.Fit(context.Background(), []classify.Example{
		{Text: "the build fails on CI", Labels: map[string]string{"topic": "build"}},
		{Text: "compile errors in the pipeline", Labels: map[string]string{"topic": "build"}},
		{Text: "book a flight to Paris", Labels: map[string]string{"topic": "travel"}},
		{Text: "find a hotel for the trip", Labels: map[string]string{"topic": "travel"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for text, want := range map[string]string{
		"the CI job is red again":         "build",
		"I need a train ticket to Berlin": "travel",
	} {
		votes, err := c.Judge(context.Background(), classify.Input{Text: text})
		if err != nil {
			t.Fatal(err)
		}
		best, bw := "", math.Inf(-1)
		for _, v := range votes {
			if v.Weight > bw {
				best, bw = v.Label, v.Weight
			}
		}
		if best != want {
			t.Errorf("%q: %s, want %s (%v)", text, best, want, votes)
		}
	}
}
