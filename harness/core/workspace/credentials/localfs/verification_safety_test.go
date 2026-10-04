//go:build linux || darwin

package localfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/credentials"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

// replaceLinkNewInode swaps a symlink for another one with the SAME target and a DIFFERENT inode.
func replaceLinkNewInode(t *testing.T, path string) {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.Symlink(target, tmp); err != nil { // created while the old inode is still alive
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestSafetyLinkSwappedBetweenCreateAndVerify(t *testing.T) {
	g, c, p, _ := realFixture(t)
	sink := &hookSink{}
	n := 0
	sink.hook = func(_ int, e effects.Evidence) error {
		if e.Phase == effects.LinkCreatedPhase {
			n++
			if n == 3 { // after the last create, before verification
				replaceLinkNewInode(t, filepath.Join(g.Candidate.Path, "a"))
			}
		}
		return nil
	}
	c.Receipts = sink
	before := snapshot(t, g.Candidate.Path)
	r := runApply(t, g, c, p)
	reality(t, g, r, before, "swap-between-create-and-verify")
	if r.Code == "group_complete" {
		t.Fatalf("verification accepted a swapped link: %+v", r.Code)
	}
	if _, ok := snapshot(t, g.Candidate.Path)["a"]; !ok {
		t.Fatalf("swapped (not ours) link was removed by compensation")
	}
}

func TestSafetyLinkSwappedBetweenVerifyAndCompensate(t *testing.T) {
	g, c, p, _ := realFixture(t)
	sink := &hookSink{}
	sink.hook = func(_ int, e effects.Evidence) error {
		if e.Phase == effects.CompletePhase {
			replaceLinkNewInode(t, filepath.Join(g.Candidate.Path, "b"))
			return errors.New("sink down") // forces compensation after full verification
		}
		return nil
	}
	c.Receipts = sink
	before := snapshot(t, g.Candidate.Path)
	r := runApply(t, g, c, p)
	reality(t, g, r, before, "swap-between-verify-and-compensate")
	after := snapshot(t, g.Candidate.Path)
	if _, ok := after["b"]; !ok {
		t.Fatalf("swapped link removed")
	}
	if _, ok := after["a"]; ok {
		t.Fatalf("our link a not compensated")
	}
	if r.Outcome != effects.Partial {
		t.Fatalf("expected Partial: %s/%s", r.Outcome, r.Code)
	}
}

func TestSafetyInspectCompleteEvidenceWithMissingLink(t *testing.T) {
	g, c, p, _ := realFixture(t)
	r := runApply(t, g, c, p)
	os.Remove(filepath.Join(g.Candidate.Path, "b"))
	x := credentials.Inspect(context.Background(), g, r.Evidence, c.PreflightContext, p)
	t.Logf("complete evidence + link vanished: %s/%s states=%v", x.Outcome, x.Code, x.Inspections)
	if x.Code == "inspected_complete" || x.Outcome == effects.AlreadyPresent {
		t.Errorf("vanished link still reported complete")
	}
}

func TestSafetyInspectParentSwapDivergent(t *testing.T) {
	g, c, p, _ := realFixture(t)
	g.Bindings[1].Destination = "nested/b"
	g.Bindings[2].Destination = "nested/c"
	os.Mkdir(filepath.Join(g.Candidate.Path, "nested"), 0700)
	r := runApply(t, g, c, p)
	if r.Outcome != effects.Applied {
		t.Fatal(r)
	}
	// replace the parent directory with an identical-looking copy (new inode, same link text)
	nested := filepath.Join(g.Candidate.Path, "nested")
	os.Rename(nested, nested+"-old")
	os.Mkdir(nested, 0700)
	for _, name := range []string{"b", "c"} {
		tgt, _ := os.Readlink(filepath.Join(nested+"-old", name))
		os.Symlink(tgt, filepath.Join(nested, name))
	}
	x := credentials.Inspect(context.Background(), g, r.Evidence, c.PreflightContext, p)
	t.Logf("parent replaced by lookalike: %s/%s states=%v", x.Outcome, x.Code, x.Inspections)
	if x.Code == "inspected_complete" {
		t.Errorf("lookalike parent with recreated links classified as complete/intended")
	}
}
