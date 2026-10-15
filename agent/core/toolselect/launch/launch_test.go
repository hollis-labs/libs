package launch_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	contracts "github.com/hollis-labs/substrate/llm-core/contracts"

	"github.com/hollis-labs/substrate/agent/toolselect/launch"
	"github.com/hollis-labs/substrate/agent/toolselect/profile"
)

func assignment(allow, deny []string) contracts.Assignment {
	return contracts.Assignment{Grants: contracts.Grants{MCP: contracts.MCPGrant{Allow: allow, Deny: deny}}}
}

func catalog() profile.Catalog {
	tool := func(n string) profile.Tool { return profile.Tool{Name: n} }
	return profile.Catalog{Servers: []profile.Server{
		{ID: "github", Tools: []profile.Tool{tool("github_issue_get"), tool("github_issue_list"), tool("github_pr_merge")}},
		{ID: "shell", Tools: []profile.Tool{tool("shell_exec"), tool("shell_env")}},
		{ID: "files", Tools: []profile.Tool{tool("files_read"), tool("files_write")}},
	}}
}

func visibleNames(t *testing.T, p profile.Profile) []string {
	t.Helper()
	vis, _, err := profile.Evaluate(catalog(), p)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range vis {
		names = append(names, v.Name)
	}
	slices.Sort(names)
	return names
}

func TestZeroGrantsReturnBaseUnchanged(t *testing.T) {
	yes := true
	base := profile.Profile{
		ID: "dev", Servers: map[string]bool{"shell": false}, ToolsAllow: []string{"github_*"},
		ToolsDeny: []string{"github_pr_merge"}, ReadOnly: yes, Order: []string{"github_issue_get"},
		AlwaysLoad: []string{"files_read"}, Instructions: "be careful",
	}
	got, err := launch.FromAssignment(base, contracts.Assignment{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, base) {
		t.Fatalf("got %+v, want %+v", got, base)
	}
}

func TestZeroBaseAndZeroGrantsShowEverything(t *testing.T) {
	got, err := launch.FromAssignment(profile.Profile{}, contracts.Assignment{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(visibleNames(t, got)); n != 7 {
		t.Fatalf("visible = %d, want 7", n)
	}
}

func TestGrantDenyIsUnionedWithBaseDeny(t *testing.T) {
	base := profile.Profile{ToolsDeny: []string{"shell_exec"}}
	got, err := launch.FromAssignment(base, assignment(nil, []string{"shell_exec", "files_write"}))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"shell_exec", "files_write"}; !slices.Equal(got.ToolsDeny, want) {
		t.Fatalf("deny = %v, want %v", got.ToolsDeny, want)
	}
	for _, n := range visibleNames(t, got) {
		if n == "shell_exec" || n == "files_write" {
			t.Fatalf("%s should be hidden", n)
		}
	}
}

func TestGrantAllowBecomesAllowWhenBaseAllowsEverything(t *testing.T) {
	got, err := launch.FromAssignment(profile.Profile{}, assignment([]string{"github_issue_*"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"github_issue_get", "github_issue_list"}
	if v := visibleNames(t, got); !slices.Equal(v, want) {
		t.Fatalf("visible = %v, want %v", v, want)
	}
}

func TestBaseAllowKeptWhenGrantHasNoAllow(t *testing.T) {
	base := profile.Profile{ToolsAllow: []string{"files_*"}}
	got, err := launch.FromAssignment(base, assignment(nil, []string{"files_write"}))
	if err != nil {
		t.Fatal(err)
	}
	if v := visibleNames(t, got); !slices.Equal(v, []string{"files_read"}) {
		t.Fatalf("visible = %v", v)
	}
}

func TestAllowIntersection(t *testing.T) {
	cases := []struct {
		name        string
		base, grant []string
		want        []string
	}{
		{"literal inside grant glob", []string{"github_issue_get", "shell_exec"}, []string{"github_*"}, []string{"github_issue_get"}},
		{"literal inside base glob", []string{"github_*"}, []string{"github_pr_merge", "files_read"}, []string{"github_pr_merge"}},
		{"identical globs", []string{"github_*"}, []string{"github_*"}, []string{"github_issue_get", "github_issue_list", "github_pr_merge"}},
		{"overlapping globs are dropped, never widened", []string{"github_*"}, []string{"github_issue_*"}, []string{"__nothing__"}},
		{"disjoint is empty", []string{"shell_*"}, []string{"files_*"}, []string{"__nothing__"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := launch.FromAssignment(profile.Profile{ToolsAllow: tc.base}, assignment(tc.grant, nil))
			if err != nil {
				t.Fatal(err)
			}
			v := visibleNames(t, got)
			if tc.want[0] == "__nothing__" {
				if len(v) != 0 {
					t.Fatalf("visible = %v, want none", v)
				}
				return
			}
			if !slices.Equal(v, tc.want) {
				t.Fatalf("visible = %v, want %v", v, tc.want)
			}
		})
	}
}

func TestEmptyIntersectionNeverBecomesEmptyAllowList(t *testing.T) {
	got, err := launch.FromAssignment(profile.Profile{ToolsAllow: []string{"shell_*"}}, assignment([]string{"files_*"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ToolsAllow) == 0 {
		t.Fatal("an empty ToolsAllow allows every tool; the intersection must stay non-empty")
	}
}

// TestNeverWidensBase is the ceiling contract: whatever the grant, the derived
// profile shows no tool the base hides, and hides at least what the base hides.
func TestNeverWidensBase(t *testing.T) {
	patterns := []string{"github_*", "github_issue_*", "github_issue_get", "shell_exec", "shell_*", "*", "files_?ead", "nomatch", "[gs]*_*"}
	lists := [][]string{nil}
	for _, p := range patterns {
		lists = append(lists, []string{p})
		for _, q := range patterns {
			lists = append(lists, []string{p, q})
		}
	}
	bases := []profile.Profile{{}, {ToolsDeny: []string{"files_*"}}, {ToolsAllow: []string{"github_*", "files_read"}}, {ToolsAllow: []string{"*"}, ToolsDeny: []string{"shell_env"}}}
	for _, base := range bases {
		baseVisible := visibleNames(t, base)
		for _, allow := range lists {
			for _, deny := range lists {
				got, err := launch.FromAssignment(base, assignment(allow, deny))
				if err != nil {
					t.Fatal(err)
				}
				for _, n := range visibleNames(t, got) {
					if !slices.Contains(baseVisible, n) {
						t.Fatalf("base %+v allow %v deny %v: %s visible but hidden by base", base, allow, deny, n)
					}
				}
			}
		}
	}
}

func TestGrantDoesNotWidenReadOnlyOrServers(t *testing.T) {
	base := profile.Profile{ReadOnly: true, Servers: map[string]bool{"shell": false}}
	got, err := launch.FromAssignment(base, assignment([]string{"*"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !got.ReadOnly || got.Servers["shell"] {
		t.Fatalf("read-only or server switch relaxed: %+v", got)
	}
}

func TestBadGlobIsErrBadPattern(t *testing.T) {
	for _, a := range []contracts.Assignment{assignment([]string{"["}, nil), assignment(nil, []string{"a["})} {
		if _, err := launch.FromAssignment(profile.Profile{}, a); !errors.Is(err, profile.ErrBadPattern) {
			t.Fatalf("err = %v, want ErrBadPattern", err)
		}
	}
}

func TestBaseIsNotMutatedOrAliased(t *testing.T) {
	base := profile.Profile{
		Servers: map[string]bool{"files": true}, ToolsAllow: []string{"files_*"}, ToolsDeny: []string{"a"},
		Order: []string{"files_read"}, AlwaysLoad: []string{"files_read"},
	}
	snapshot := profile.Profile{
		Servers: map[string]bool{"files": true}, ToolsAllow: []string{"files_*"}, ToolsDeny: []string{"a"},
		Order: []string{"files_read"}, AlwaysLoad: []string{"files_read"},
	}
	got, err := launch.FromAssignment(base, assignment([]string{"files_read"}, []string{"b"}))
	if err != nil {
		t.Fatal(err)
	}
	got.Servers["files"] = false
	got.ToolsAllow[0] = "x"
	got.ToolsDeny[0] = "x"
	got.Order[0] = "x"
	got.AlwaysLoad[0] = "x"
	if !reflect.DeepEqual(base, snapshot) {
		t.Fatalf("base changed: %+v", base)
	}
}

func TestGrantSlicesAreNotAliased(t *testing.T) {
	grant := []string{"files_*"}
	got, err := launch.FromAssignment(profile.Profile{}, assignment(grant, nil))
	if err != nil {
		t.Fatal(err)
	}
	got.ToolsAllow[0] = "x"
	if grant[0] != "files_*" {
		t.Fatal("result aliases the assignment's grant")
	}
}

func TestDeterministic(t *testing.T) {
	base := profile.Profile{ToolsAllow: []string{"github_*", "files_read", "shell_env"}, ToolsDeny: []string{"a"}}
	a := assignment([]string{"files_*", "shell_env", "github_*"}, []string{"b", "a"})
	first, err := launch.FromAssignment(base, a)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, _ := launch.FromAssignment(base, a)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("got %+v then %+v", first, again)
		}
	}
}
