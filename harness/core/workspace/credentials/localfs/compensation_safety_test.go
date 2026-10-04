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

func TestSafetyCompensationRunsInReverseCreationOrder(t *testing.T) {
	g, c, p, _ := realFixture(t)
	f := &rmFailPort{LinkPort: p, removeFail: map[int]bool{}, createFail: 3}
	r := runApply(t, g, c, f)
	if r.Outcome != effects.Partial {
		t.Fatal(r)
	}
	if len(f.removedOrder) != 2 || f.removedOrder[0] != "b" || f.removedOrder[1] != "a" {
		t.Fatalf("compensation order %v, want [b a]", f.removedOrder)
	}
}

func TestSafetyGroupWritableIntermediateParentRefused(t *testing.T) {
	g, c, p, _ := realFixture(t)
	g.Bindings[2].Destination = "nested/c"
	nested := filepath.Join(g.Candidate.Path, "nested")
	os.Mkdir(nested, 0700)
	os.Chmod(nested, 0777)
	_, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
	if r.Code == "preflight_complete" {
		t.Fatalf("world-writable destination parent accepted")
	}
}

func TestSafetyRelativeLinkTargetRefusedByPort(t *testing.T) {
	g, _, p, _ := realFixture(t)
	s, e := p.OpenCandidate(context.Background(), g.Candidate)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e := s.CreateExclusive(context.Background(), "relative/target", "x"); e == nil {
		t.Fatalf("port created a link with a relative target")
	}
}

// Same link inodes, different parent directory (links renamed into a new directory): must be divergent.
func TestSafetyInspectSameLinkInodesNewParent(t *testing.T) {
	g, c, p, _ := realFixture(t)
	g.Bindings[1].Destination = "nested/b"
	g.Bindings[2].Destination = "nested/c"
	nested := filepath.Join(g.Candidate.Path, "nested")
	os.Mkdir(nested, 0700)
	r := runApply(t, g, c, p)
	if r.Outcome != effects.Applied {
		t.Fatal(r)
	}
	os.Mkdir(nested+"-new", 0700)
	for _, n := range []string{"b", "c"} {
		if err := os.Rename(filepath.Join(nested, n), filepath.Join(nested+"-new", n)); err != nil {
			t.Fatal(err)
		}
	}
	os.Remove(nested)
	os.Rename(nested+"-new", nested)
	x := credentials.Inspect(context.Background(), g, r.Evidence, c.PreflightContext, p)
	t.Logf("links moved into a new parent: %s/%s states=%v", x.Outcome, x.Code, x.Inspections)
	if x.Code == "inspected_complete" {
		t.Errorf("same link inodes under a different parent classified complete")
	}
}
