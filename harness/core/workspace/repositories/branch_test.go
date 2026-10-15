package repositories

import "testing"

func TestBranchResolutionIsConcrete(t *testing.T) {
	b, e := ResolveBranch("work/${agent}/${assignment}", BranchVariables{Agent: "fixture", Assignment: "change"})
	if e != nil || b != "work/fixture/change" {
		t.Fatal(b, e)
	}
	for _, template := range []string{"", "${missing}", "${instance}", "${agent", "${agent}/../main", "${agent}/x.lock", "${agent}/@{x}", "${agent}/a b", "${agent}/--"} {
		_, e := ResolveBranch(template, BranchVariables{Agent: "fixture"})
		if template == "${agent}/--" {
			if e != nil {
				t.Fatal(e)
			}
			continue
		}
		if e == nil {
			t.Fatal("unresolved or invalid branch accepted", template)
		}
	}
	_, e = ResolveBranch("${agent}", BranchVariables{Agent: "${assignment}", Assignment: "main"})
	if e == nil {
		t.Fatal("nested expansion accepted")
	}
}
func TestBranchRules(t *testing.T) {
	for _, b := range []string{"main", "work/fixture", "feature/é", "release-1"} {
		if !ValidBranch(b) {
			t.Fatal(b)
		}
	}
	for _, b := range []string{"", "@", "-main", "a.", "a..b", "a@{b}", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b", "a b", "a\n", "a\x00", "/a", "a/", "a//b", ".a", "a/.b", "a.lock", "a/b.lock", "a/${missing}"} {
		if ValidBranch(b) {
			t.Fatal("invalid branch accepted", b)
		}
	}
}
