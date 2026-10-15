//go:build linux || darwin

package localfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCompensationNeverRemovesNonLinks(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular", true: "directory"}[directory], func(t *testing.T) {
			g, _, p, _ := realFixture(t)
			path := filepath.Join(g.Candidate.Path, "artifact")
			if directory {
				if e := os.Mkdir(path, 0700); e != nil {
					t.Fatal(e)
				}
			} else {
				if e := os.WriteFile(path, []byte("artifact"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			s, e := p.OpenCandidate(context.Background(), g.Candidate)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			observed, e := s.Inspect(context.Background(), "artifact")
			if e != nil {
				t.Fatal(e)
			}
			if !observed.Exists || observed.IsLink {
				t.Fatal(observed)
			}
			if e := s.RemoveIfMatches(context.Background(), "artifact", observed); e == nil {
				t.Fatal("non-link accepted for compensation")
			}
			if _, e := os.Lstat(path); e != nil {
				t.Fatal("artifact removed", e)
			}
		})
	}
}
func TestCompensationReportsFilesystemRemoveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses parent write permissions")
	}
	g, _, p, _ := realFixture(t)
	parent := filepath.Join(g.Candidate.Path, "sealed")
	if e := os.Mkdir(parent, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(filepath.Join(g.Home.LogicalPath, "a"), filepath.Join(parent, "a")); e != nil {
		t.Fatal(e)
	}
	s, e := p.OpenCandidate(context.Background(), g.Candidate)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	observed, e := s.Inspect(context.Background(), "sealed/a")
	if e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(parent, 0555); e != nil {
		t.Fatal(e)
	}
	defer os.Chmod(parent, 0700)
	if e := s.RemoveIfMatches(context.Background(), "sealed/a", observed); e == nil {
		t.Fatal("failed removal reported success")
	}
	if _, e := os.Lstat(filepath.Join(parent, "a")); e != nil {
		t.Fatal("entry unexpectedly removed", e)
	}
}
