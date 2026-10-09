package toolselect_test

import (
	"reflect"
	"testing"

	toolselect "github.com/hollis-labs/substrate/agent/toolselect"
)

func TestLiteralNameLeadsExactTierGolden(t *testing.T) {
	catalog := toolselect.Catalog{Tools: []toolselect.Tool{
		{Name: "aaa_title", Title: "zzz_tool"},
		{Name: "ZZZ_TOOL"},
		{Name: "zzz_tool"},
	}}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"zzz_tool", []string{"zzz_tool", "ZZZ_TOOL", "aaa_title"}},
		{"  ZZZ_TOOL  ", []string{"ZZZ_TOOL", "zzz_tool", "aaa_title"}},
		{"zZz_ToOl", []string{"ZZZ_TOOL", "zzz_tool", "aaa_title"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			for reverse := 0; reverse < 2; reverse++ {
				hits := mustRank(t, catalog, tc.query, nil)
				if got := names(hits); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
				for _, hit := range hits {
					if hit.Tier != toolselect.TierExactName || hit.Score != 0 {
						t.Fatalf("exact match: %+v", hit)
					}
				}
				limited := mustRank(t, catalog, tc.query, nil, toolselect.WithMaxResults(1))
				if limited[0].Tool.Name != tc.want[0] {
					t.Fatalf("limited: %+v", limited)
				}
				catalog.Tools[0], catalog.Tools[2] = catalog.Tools[2], catalog.Tools[0]
			}
		})
	}
}
