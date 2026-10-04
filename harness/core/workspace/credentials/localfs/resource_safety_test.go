//go:build linux || darwin

package localfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/credentials"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

// The provider home is a symlink; it is retargeted (to a lookalike tree) between Preflight and Apply.
func TestSafetyHomeSymlinkRetargetedAfterPreflight(t *testing.T) {
	g, c, p, _ := realFixture(t)
	base := filepath.Dir(g.Home.LogicalPath)
	real := g.Home.LogicalPath
	link := filepath.Join(base, "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	g.Home.LogicalPath = link // logical path is the link, canonical remains the real dir
	prepared, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
	if r.Code != "preflight_complete" {
		t.Fatalf("preflight: %+v", r)
	}
	other := filepath.Join(base, "other-home")
	os.Mkdir(other, 0700)
	for _, n := range []string{"a", "b", "c"} {
		os.WriteFile(filepath.Join(other, n), []byte("x"), 0600)
	}
	os.Remove(link)
	os.Symlink(other, link)
	x := credentials.Apply(context.Background(), prepared, c, p)
	if x.Code == "group_complete" {
		t.Fatalf("apply followed a retargeted home: %+v", x.Code)
	}
	if len(snapshot(t, g.Candidate.Path)) != 0 {
		t.Fatalf("links created from retargeted home")
	}
}

// Two-level destination whose FIRST component is a symlink to a sibling directory inside the candidate.
func TestSafetyTwoLevelParentSymlinkComponent(t *testing.T) {
	g, c, p, _ := realFixture(t)
	g.Bindings[2].Destination = "x/y/c"
	os.MkdirAll(filepath.Join(g.Candidate.Path, "z", "y"), 0700)
	os.Symlink("z", filepath.Join(g.Candidate.Path, "x"))
	_, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
	if r.Code == "preflight_complete" {
		t.Fatalf("symlink intermediate component accepted by preflight")
	}
}

// Future (absent) candidate whose parent is a symlink leaving the mutation identity.
func TestSafetyFutureCandidateViaEscapingParent(t *testing.T) {
	g, c, p, _ := realFixture(t)
	ident := g.Candidate.MutationIdentity
	outside := filepath.Join(filepath.Dir(ident), "sibling-inside-allowed-base")
	os.Mkdir(outside, 0700)
	os.Symlink(outside, filepath.Join(ident, "esc"))
	g.Candidate.AllowedBase = filepath.Dir(ident) // wider than the mutation identity
	g.Candidate.Path = filepath.Join(ident, "esc", "cand")
	_, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
	if r.Code == "preflight_complete" {
		t.Fatalf("future candidate beneath an escaping symlink accepted by preflight")
	}
}

// Replay on a fully applied group, with every row recorded: never creates, never removes, never claims Created.
func TestSafetyReplayNeverRecreatesOrClaimsCreated(t *testing.T) {
	g, c, p, _ := realFixture(t)
	first := runApply(t, g, c, p)
	snap := snapshot(t, g.Candidate.Path)
	sink := &hookSink{}
	c.Receipts = sink
	r := runApply(t, g, c, p)
	if r.Outcome != effects.AlreadyPresent {
		t.Fatalf("replay outcome %s/%s", r.Outcome, r.Code)
	}
	for _, l := range r.Evidence.Links {
		if l.Created {
			t.Fatalf("replay claims Created: %+v", l)
		}
	}
	for _, row := range sink.rows {
		for _, l := range row.Links {
			if l.Created {
				t.Fatalf("replay row claims Created")
			}
		}
	}
	after := snapshot(t, g.Candidate.Path)
	for k, v := range snap {
		if after[k] != v {
			t.Fatalf("replay changed %s", k)
		}
	}
	_ = first
}

// Retry after a partial failure that retained a link: the retained link is adopted as pre-existing (not Created) and survives a later failure.
func TestSafetyRetryAdoptsRetainedLinkAndNeverRemovesIt(t *testing.T) {
	g, c, p, _ := realFixture(t)
	f := &rmFailPort{LinkPort: p, removeFail: map[int]bool{1: true, 2: true, 3: true}, createFail: 3}
	r1 := runApply(t, g, c, f) // a,b created, c fails, compensation fails -> both retained
	if r1.Outcome != effects.Partial {
		t.Fatal(r1)
	}
	retained := snapshot(t, g.Candidate.Path)
	f2 := &rmFailPort{LinkPort: p, removeFail: map[int]bool{}, createFail: 2} // retry: first create (c) ok? fail the 1st create after adoption
	f2.createFail = 1
	r2 := runApply(t, g, c, f2)
	after := snapshot(t, g.Candidate.Path)
	for k, v := range retained {
		if after[k] != v {
			t.Errorf("retained link %s was removed/altered by a retry's compensation", k)
		}
	}
	t.Logf("retry outcome=%s/%s obligations=%v", r2.Outcome, r2.Code, r2.Obligations)
}
