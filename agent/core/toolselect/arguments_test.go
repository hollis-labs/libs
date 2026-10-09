package toolselect_test

import (
	"math"
	"reflect"
	"testing"

	toolselect "github.com/hollis-labs/substrate/agent/toolselect"
)

func argumentCatalog() toolselect.Catalog {
	return toolselect.Catalog{Tools: []toolselect.Tool{
		{Name: "alpha", Arguments: []toolselect.Argument{{Name: "filepath", Description: "archive snapshot"}}},
		{Name: "beta", Arguments: []toolselect.Argument{{Name: "target", Description: "restore snapshot snapshot"}}},
		{Name: "gamma", Description: "restore archive"},
	}}
}

func TestArgumentMetadataBM25Golden(t *testing.T) {
	// The documents have lengths 4, 5 and 3, avgdl=4. Archive, snapshot and
	// restore each have df=2; filepath has df=1. Expected scores use the BM25
	// formula independently, not the index's internals.
	for _, tc := range []struct {
		query  string
		want   []string
		scores []float64
	}{
		{"filepath", []string{"alpha"}, []float64{0.9808292530117263}},
		{"the filepath", []string{"alpha"}, []float64{0.9808292530117263}},
		{"archive snapshot", []string{"alpha", "beta", "gamma"}, []float64{0.9400072584914713, 0.6038002828266386, 0.523548346501579}},
		{"restore snapshot", []string{"beta", "gamma", "alpha"}, []float64{1.0301953279155534, 0.523548346501579, 0.47000362924573563}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			c := argumentCatalog()
			for reverse := 0; reverse < 2; reverse++ {
				hits := mustRank(t, c, tc.query, nil)
				if got := names(hits); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
				for i, hit := range hits {
					if hit.Tier != toolselect.TierBM25 || math.Abs(hit.Score-tc.scores[i]) > 1e-12 {
						t.Errorf("%s: %+v, want score %.16g", hit.Tool.Name, hit, tc.scores[i])
					}
				}
				c.Tools[0], c.Tools[2] = c.Tools[2], c.Tools[0]
			}
		})
	}
}

func TestIndexCopiesArgumentMetadata(t *testing.T) {
	c := argumentCatalog()
	idx, err := toolselect.NewIndex(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Tools[0].Arguments[0] = toolselect.Argument{Name: "changed", Description: "changed"}
	hits, err := idx.Rank("filepath", nil)
	if err != nil || len(hits) != 1 {
		t.Fatalf("rank: %+v, %v", hits, err)
	}
	if got := hits[0].Tool.Arguments[0]; got.Name != "filepath" || got.Description != "archive snapshot" {
		t.Fatalf("caller aliased index: %+v", got)
	}
	hits[0].Tool.Arguments[0] = toolselect.Argument{Name: "changed again"}
	again, err := idx.Rank("filepath", nil)
	if err != nil || !reflect.DeepEqual(again[0].Tool.Arguments, argumentCatalog().Tools[0].Arguments) {
		t.Fatalf("hit aliased index: %+v, %v", again, err)
	}
}
