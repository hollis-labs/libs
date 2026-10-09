package toolselect_test

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	toolselect "github.com/hollis-labs/substrate/agent/toolselect"
)

func names(hits []toolselect.Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Tool.Name
	}
	return out
}

func mustRank(t *testing.T, c toolselect.Catalog, q string, rules []toolselect.Rule, opts ...toolselect.Option) []toolselect.Hit {
	t.Helper()
	hits, err := toolselect.Rank(c, q, rules, opts...)
	if err != nil {
		t.Fatalf("Rank(%q): %v", q, err)
	}
	return hits
}

func goldenCatalog() toolselect.Catalog {
	return toolselect.Catalog{Tools: []toolselect.Tool{
		{Server: "a", Name: "task_create", Description: "Create a new task in the tracker", Tags: []string{"alpha"}},
		{Server: "a", Name: "task_list", Description: "List every task", Tags: []string{"alpha"}},
		{Server: "b", Name: "file_read", Description: "Read a file from disk", Tags: []string{"beta"}},
		{Server: "c", Name: "note_search", Description: "Search notes for a keyword; task related", Tags: []string{"gamma"}},
	}}
}

// Expected values come from an independent implementation of
// idf = ln(1 + (N-df+0.5)/(df+0.5)), score = sum idf*tf*(k1+1)/(tf + k1*(1-b+b*dl/avgdl)).
func TestBM25Golden(t *testing.T) {
	// "create task" is not a tool name or prefix, so all tiers are BM25.
	hits := mustRank(t, goldenCatalog(), "create task", nil)
	want := map[string]float64{
		"task_create": 2.0630021736717508,
		"task_list":   0.5109575941075796,
		"note_search": 0.3369812353776982,
	}
	if len(hits) != len(want) {
		t.Fatalf("got %v, want %d hits", names(hits), len(want))
	}
	for _, h := range hits {
		if h.Tier != toolselect.TierBM25 {
			t.Errorf("%s: tier %v", h.Tool.Name, h.Tier)
		}
		if math.Abs(h.Score-want[h.Tool.Name]) > 1e-12 {
			t.Errorf("%s: score %v, want %v", h.Tool.Name, h.Score, want[h.Tool.Name])
		}
	}
	if got := names(hits); !reflect.DeepEqual(got, []string{"task_create", "task_list", "note_search"}) {
		t.Errorf("order %v", got)
	}
	hits = mustRank(t, goldenCatalog(), "read file", nil)
	if len(hits) != 1 || math.Abs(hits[0].Score-3.4495220812315197) > 1e-12 {
		t.Fatalf("read file: %+v", hits)
	}
}

func TestBM25NonMatchingToolsAreOmitted(t *testing.T) {
	hits := mustRank(t, goldenCatalog(), "zzzz", nil)
	if len(hits) != 0 {
		t.Fatalf("got %v", names(hits))
	}
}

func stuffedCatalog() toolselect.Catalog {
	return toolselect.Catalog{Tools: []toolselect.Tool{
		{Name: "aaa_widget_helper", Description: strings.Repeat("deploy service now ", 12)},
		{Name: "deploy", Description: "x"},
		{Name: "deploy_service_extra", Description: "y"},
		{Name: "zzz_other", Description: "deploy deploy deploy"},
	}}
}

func TestTierExactNameOutranksStuffedDescription(t *testing.T) {
	for _, k1 := range []float64{0, 0.5, 1.2, 3, 10} {
		for _, b := range []float64{0, 0.25, 0.75, 1} {
			hits := mustRank(t, stuffedCatalog(), "deploy", nil, toolselect.WithK1B(k1, b))
			if len(hits) < 3 {
				t.Fatalf("k1=%v b=%v: %v", k1, b, names(hits))
			}
			if hits[0].Tool.Name != "deploy" || hits[0].Tier != toolselect.TierExactName || hits[0].Score != 0 {
				t.Errorf("k1=%v b=%v: first hit %+v", k1, b, hits[0])
			}
			for _, h := range hits[1:] {
				if h.Tier == toolselect.TierExactName {
					t.Errorf("k1=%v b=%v: second exact %v", k1, b, h.Tool.Name)
				}
			}
		}
	}
}

func TestTierExactMatchesTitleAndIgnoresCase(t *testing.T) {
	c := toolselect.Catalog{Tools: []toolselect.Tool{
		{Name: "aaa", Description: "Build Project Build Project"},
		{Name: "zzz_x", Title: "Build Project", Description: "unrelated"},
	}}
	hits := mustRank(t, c, "  build PROJECT ", nil)
	if hits[0].Tool.Name != "zzz_x" || hits[0].Tier != toolselect.TierExactName {
		t.Fatalf("got %+v", hits)
	}
}

func TestTierPrefixOutranksBM25AndFollowsExact(t *testing.T) {
	for _, k1 := range []float64{0, 1.2, 10} {
		for _, b := range []float64{0, 0.75, 1} {
			hits := mustRank(t, stuffedCatalog(), "deploy_serv", nil, toolselect.WithK1B(k1, b))
			// "deploy_serv" prefixes deploy_service_extra; "deploy" is a prefix of the query.
			if len(hits) < 2 {
				t.Fatalf("%v", names(hits))
			}
			for _, h := range hits[:2] {
				if h.Tier != toolselect.TierPrefix || h.Score != 0 {
					t.Errorf("k1=%v b=%v: %+v", k1, b, h)
				}
			}
			if got := names(hits[:2]); !reflect.DeepEqual(got, []string{"deploy", "deploy_service_extra"}) {
				t.Errorf("prefix tier order %v", got)
			}
			for _, h := range hits[2:] {
				if h.Tier != toolselect.TierBM25 {
					t.Errorf("tail tier %+v", h)
				}
			}
		}
	}
	// exact ahead of prefix
	hits := mustRank(t, stuffedCatalog(), "deploy", nil)
	if hits[0].Tier != toolselect.TierExactName || hits[1].Tier != toolselect.TierPrefix {
		t.Errorf("tiers %v %v", hits[0].Tier, hits[1].Tier)
	}
}

func TestActionOrderPinsLeadInPinOrder(t *testing.T) {
	rules := []toolselect.Rule{{
		Name:   "pin",
		Match:  toolselect.Match{Patterns: []string{"file_*", "note_*"}},
		Action: toolselect.Action{Type: toolselect.ActionOrder, Pin: []string{"note_search", "file_read", "ghost", "task_list"}},
	}}
	hits := mustRank(t, goldenCatalog(), "create task", rules)
	got := names(hits)
	// task_list is in Pin but not matched by the rule, so it is not pinned; ghost is absent.
	want := []string{"note_search", "file_read", "task_create", "task_list"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if hits[0].Tier != toolselect.TierPinned || hits[1].Tier != toolselect.TierPinned || hits[0].Score != 0 {
		t.Errorf("tiers: %+v", hits[:2])
	}
	if hits[2].Tier != toolselect.TierBM25 {
		t.Errorf("tier %v", hits[2].Tier)
	}
}

func TestActionOrderRespectsExclusionAndNeverAddsMembers(t *testing.T) {
	rules := []toolselect.Rule{
		{Match: toolselect.Match{Patterns: []string{"note_*"}}, Action: toolselect.Action{Type: toolselect.ActionExclude}},
		{Action: toolselect.Action{Type: toolselect.ActionOrder, Pin: []string{"note_search", "file_read"}}},
	}
	got := names(mustRank(t, goldenCatalog(), "create task", rules))
	want := []string{"file_read", "task_create", "task_list"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestActionOrderMultipleRulesFollowPriority(t *testing.T) {
	rules := []toolselect.Rule{
		{Priority: 1, Action: toolselect.Action{Type: toolselect.ActionOrder, Pin: []string{"task_list"}}},
		{Priority: 5, Action: toolselect.Action{Type: toolselect.ActionOrder, Pin: []string{"task_create", "task_list"}}},
	}
	got := names(mustRank(t, goldenCatalog(), "task", rules))[:2]
	if !reflect.DeepEqual(got, []string{"task_create", "task_list"}) {
		t.Fatalf("got %v", got)
	}
}

func ruleCatalog() toolselect.Catalog {
	mk := func(server, name string, tags ...string) toolselect.Tool {
		return toolselect.Tool{Server: server, Name: name, Description: "manage things", Tags: tags}
	}
	return toolselect.Catalog{Tools: []toolselect.Tool{
		mk("hadron", "hadron_bp_get"), mk("hadron", "hadron_bp_list"),
		mk("volon", "volon_task_get"), mk("volon", "volon_task_create"),
		mk("volon", "volon_tasks_list"), mk("volon", "volon_note_add"),
		mk("misc", "misc_thing"),
	}}
}

// Port of go-toolbroker's TestSelectToolsCombinedExcludeAndInclude: an
// exclude and an include both apply and the combined outcome is pinned.
func TestSelectToolsCombinedExcludeAndInclude(t *testing.T) {
	rules := []toolselect.Rule{
		{Name: "exclude-bp", Intent: "*", Priority: 10,
			Match: toolselect.Match{Patterns: []string{"hadron_bp_*"}}, Action: toolselect.Action{Type: toolselect.ActionExclude}},
		{Name: "include-volon", Intent: "manage*", Priority: 20,
			Match: toolselect.Match{Patterns: []string{"volon_task_*", "volon_tasks_list"}}, Action: toolselect.Action{Type: toolselect.ActionInclude}},
	}
	got := names(mustRank(t, ruleCatalog(), "manage things", rules))
	sort.Strings(got)
	want := []string{"volon_task_create", "volon_task_get", "volon_tasks_list"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Intent that does not match the include rule: only the exclude applies.
	got = names(mustRank(t, ruleCatalog(), "things", rules))
	if len(got) != 5 {
		t.Fatalf("got %v", got)
	}
	for _, n := range got {
		if strings.HasPrefix(n, "hadron_bp_") {
			t.Errorf("excluded tool %s returned", n)
		}
	}
}

func TestExcludeWinsOverInclude(t *testing.T) {
	rules := []toolselect.Rule{
		{Match: toolselect.Match{Servers: []string{"volon"}}, Action: toolselect.Action{Type: toolselect.ActionInclude}, Priority: 1},
		{Match: toolselect.Match{Patterns: []string{"volon_task_get"}}, Action: toolselect.Action{Type: toolselect.ActionExclude}, Priority: 99},
	}
	got := names(mustRank(t, ruleCatalog(), "manage things", rules))
	for _, n := range got {
		if n == "volon_task_get" || !strings.HasPrefix(n, "volon_") {
			t.Errorf("unexpected %s in %v", n, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("got %v", got)
	}
}

func TestMatchFieldsANDAcrossORWithin(t *testing.T) {
	c := toolselect.Catalog{Tools: []toolselect.Tool{
		{Server: "s1", Name: "t_one", Description: "thing", Tags: []string{"x"}},
		{Server: "s1", Name: "t_two", Description: "thing", Tags: []string{"y"}},
		{Server: "s2", Name: "t_three", Description: "thing", Tags: []string{"x"}},
	}}
	rules := []toolselect.Rule{{
		Match:  toolselect.Match{Patterns: []string{"t_*"}, Tags: []string{"x", "z"}, Servers: []string{"s1"}},
		Action: toolselect.Action{Type: toolselect.ActionInclude},
	}}
	if got := names(mustRank(t, c, "thing", rules)); !reflect.DeepEqual(got, []string{"t_one"}) {
		t.Fatalf("got %v", got)
	}
}

// Equal-priority rules with conflicting actions apply in declaration order.
// The order-dependent observable is which ActionOrder pin list wins first.
func TestEqualPriorityRulesApplyInDeclarationOrder(t *testing.T) {
	mk := func(pin ...string) toolselect.Rule {
		return toolselect.Rule{Priority: 7, Action: toolselect.Action{Type: toolselect.ActionOrder, Pin: pin}}
	}
	var rules []toolselect.Rule
	// 40 equal-priority rules: an unstable sort would reorder these with high probability.
	for i := 0; i < 40; i++ {
		rules = append(rules, mk(fmt.Sprintf("t%02d", i)))
	}
	var cat toolselect.Catalog
	for i := 39; i >= 0; i-- {
		cat.Tools = append(cat.Tools, toolselect.Tool{Name: fmt.Sprintf("t%02d", i), Description: "same"})
	}
	want := make([]string, 40)
	for i := range want {
		want[i] = fmt.Sprintf("t%02d", i)
	}
	for run := 0; run < 50; run++ {
		if got := names(mustRank(t, cat, "same", rules)); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: got %v", run, got)
		}
	}
}

func TestRuleValidationAndBadGlobs(t *testing.T) {
	_, err := toolselect.Rank(goldenCatalog(), "task", []toolselect.Rule{{Action: toolselect.Action{Type: "summarize"}}})
	if !errors.Is(err, toolselect.ErrInvalidRule) {
		t.Fatalf("err = %v", err)
	}
	// Bad globs never panic and match nothing.
	rules := []toolselect.Rule{
		{Intent: "[", Match: toolselect.Match{}, Action: toolselect.Action{Type: toolselect.ActionExclude}},
		{Match: toolselect.Match{Patterns: []string{"[unclosed"}}, Action: toolselect.Action{Type: toolselect.ActionExclude}},
	}
	if got := mustRank(t, goldenCatalog(), "task", rules); len(got) != 3 {
		t.Fatalf("got %v", names(got))
	}
	// A bad include glob matches nothing, so the include whitelist is empty.
	rules = []toolselect.Rule{{Match: toolselect.Match{Patterns: []string{"["}}, Action: toolselect.Action{Type: toolselect.ActionInclude}}}
	if got := mustRank(t, goldenCatalog(), "task", rules); len(got) != 0 {
		t.Fatalf("got %v", names(got))
	}
}

func TestRankIsDeterministicAndCatalogOrderIndependent(t *testing.T) {
	base := ruleCatalog()
	for i := 0; i < 30; i++ { // ties galore: identical descriptions
		base.Tools = append(base.Tools, toolselect.Tool{Name: fmt.Sprintf("bulk_%02d", i), Description: "manage things"})
	}
	want := mustRank(t, base, "manage things", nil)
	for run := 0; run < 50; run++ {
		// Reproducible permutation: sort by a per-run hash of the name.
		shuffled := toolselect.Catalog{Tools: append([]toolselect.Tool(nil), base.Tools...)}
		key := func(name string) uint32 {
			h := fnv.New32a()
			fmt.Fprintf(h, "%d/%s", run, name)
			return h.Sum32()
		}
		sort.Slice(shuffled.Tools, func(i, j int) bool { return key(shuffled.Tools[i].Name) < key(shuffled.Tools[j].Name) })
		got := mustRank(t, shuffled, "manage things", nil)
		if !reflect.DeepEqual(names(got), names(want)) {
			t.Fatalf("run %d: order differs", run)
		}
	}
	// Ties break by name ascending.
	for i := 1; i < len(want); i++ {
		if want[i-1].Score == want[i].Score && want[i-1].Tool.Name > want[i].Tool.Name {
			t.Fatalf("tie not broken by name: %s before %s", want[i-1].Tool.Name, want[i].Tool.Name)
		}
	}
}

func TestStopwordsAreDroppedFromQueryAndExtendable(t *testing.T) {
	c := toolselect.Catalog{Tools: []toolselect.Tool{
		{Name: "alpha_tool", Description: "deploy the service"},
		{Name: "beta_tool", Description: "get with make from that"},
	}}
	if got := mustRank(t, c, "the get with from", nil); len(got) != 0 {
		t.Fatalf("stopword-only query matched %v", names(got))
	}
	if got := names(mustRank(t, c, "deploy service", nil)); !reflect.DeepEqual(got, []string{"alpha_tool"}) {
		t.Fatalf("got %v", got)
	}
	if got := mustRank(t, c, "deploy", nil, toolselect.WithStopwords("Deploy")); len(got) != 0 {
		t.Fatalf("extra stopword ignored: %v", names(got))
	}
	// Extending never replaces: built-ins still apply.
	if got := mustRank(t, c, "get", nil, toolselect.WithStopwords("zzz")); len(got) != 0 {
		t.Fatalf("built-in stopword lost: %v", names(got))
	}
}

func TestOptionsValidationAndMaxResults(t *testing.T) {
	bad := []toolselect.Option{
		toolselect.WithK1B(-1, 0.5), toolselect.WithK1B(1, 1.5), toolselect.WithK1B(1, -0.1),
		toolselect.WithK1B(math.NaN(), 0.5), toolselect.WithK1B(math.Inf(1), 0.5), toolselect.WithK1B(1, math.NaN()),
		toolselect.WithMaxResults(-1),
	}
	for i, o := range bad {
		if _, err := toolselect.Rank(goldenCatalog(), "task", nil, o); !errors.Is(err, toolselect.ErrInvalidOption) {
			t.Errorf("option %d: err = %v", i, err)
		}
	}
	if got := mustRank(t, goldenCatalog(), "task", nil, toolselect.WithMaxResults(1)); len(got) != 1 {
		t.Errorf("got %d hits", len(got))
	}
	if got := mustRank(t, goldenCatalog(), "task", nil, toolselect.WithMaxResults(0)); len(got) != 3 {
		t.Errorf("got %d hits", len(got))
	}
}

func TestNewIndexRejectsBadCatalog(t *testing.T) {
	for name, c := range map[string]toolselect.Catalog{
		"empty name": {Tools: []toolselect.Tool{{Name: ""}}},
		"duplicate":  {Tools: []toolselect.Tool{{Name: "a"}, {Name: "a"}}},
	} {
		if _, err := toolselect.NewIndex(c); !errors.Is(err, toolselect.ErrInvalidCatalog) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if hits, err := toolselect.Rank(toolselect.Catalog{}, "x", nil); err != nil || len(hits) != 0 {
		t.Errorf("empty catalog: %v %v", hits, err)
	}
}

func TestIndexCopiesCatalog(t *testing.T) {
	c := goldenCatalog()
	idx, err := toolselect.NewIndex(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Tools[0].Name = "mutated"
	c.Tools[0].Tags[0] = "mutated"
	hits, _ := idx.Rank("create task", nil)
	if hits[0].Tool.Name != "task_create" || hits[0].Tool.Tags[0] != "alpha" {
		t.Fatalf("index aliased the caller's catalog: %+v", hits[0].Tool)
	}
	hits[0].Tool.Tags[0] = "changed"
	again, _ := idx.Rank("create task", nil)
	if again[0].Tool.Tags[0] != "alpha" {
		t.Fatal("returned hit aliases index state")
	}
}

func TestAnnotationsPassThroughVerbatim(t *testing.T) {
	yes, no := true, false
	c := toolselect.Catalog{Tools: []toolselect.Tool{
		{Name: "tool_undeclared", Description: "widget"},
		{Name: "tool_yes", Description: "widget", ReadOnly: &yes, Destructive: &no},
	}}
	rules := []toolselect.Rule{{Action: toolselect.Action{Type: toolselect.ActionOrder, Pin: []string{"tool_undeclared"}}}}
	for _, r := range [][]toolselect.Rule{nil, rules} {
		for _, q := range []string{"widget", "tool_undeclared", "tool"} {
			for _, h := range mustRank(t, c, q, r) {
				switch h.Tool.Name {
				case "tool_undeclared":
					if h.Tool.ReadOnly != nil || h.Tool.Destructive != nil {
						t.Errorf("q=%q: undeclared hint was set: %+v", q, h.Tool)
					}
				case "tool_yes":
					if h.Tool.ReadOnly == nil || !*h.Tool.ReadOnly || h.Tool.Destructive == nil || *h.Tool.Destructive {
						t.Errorf("q=%q: declared hints changed: %+v", q, h.Tool)
					}
				}
			}
		}
	}
}

func TestConcurrentRankOnSharedIndex(t *testing.T) {
	idx, err := toolselect.NewIndex(ruleCatalog())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := idx.Rank("manage things", nil)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				got, err := idx.Rank("manage things", nil)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Error("concurrent Rank diverged")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestTierString(t *testing.T) {
	for tier, s := range map[toolselect.MatchTier]string{
		toolselect.TierPinned: "pinned", toolselect.TierExactName: "exact_name",
		toolselect.TierPrefix: "prefix", toolselect.TierBM25: "bm25", 99: "unknown",
	} {
		if tier.String() != s {
			t.Errorf("%d: %q", tier, tier.String())
		}
	}
}

func FuzzRank(f *testing.F) {
	f.Add("create task", "task_*", "*")
	f.Add("", "[", "[a-")
	f.Add("deploy_serv", "\\", "?")
	f.Add("naïve ☃ 日本語", "*[", "**")
	idx, err := toolselect.NewIndex(ruleCatalog())
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, query, pattern, intent string) {
		rules := []toolselect.Rule{
			{Intent: intent, Match: toolselect.Match{Patterns: []string{pattern}}, Action: toolselect.Action{Type: toolselect.ActionExclude}},
			{Intent: intent, Match: toolselect.Match{Patterns: []string{pattern}}, Action: toolselect.Action{Type: toolselect.ActionInclude}, Priority: 1},
			{Action: toolselect.Action{Type: toolselect.ActionOrder, Pin: []string{pattern, "misc_thing"}}},
		}
		a, err := idx.Rank(query, rules)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := idx.Rank(query, rules)
		if !reflect.DeepEqual(a, b) {
			t.Fatal("nondeterministic")
		}
		seen := map[string]bool{}
		for _, h := range a {
			if seen[h.Tool.Name] {
				t.Fatalf("duplicate hit %s", h.Tool.Name)
			}
			seen[h.Tool.Name] = true
		}
	})
}
