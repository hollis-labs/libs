//go:build linux || darwin

package localfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceObservationBoundaries(t *testing.T) {
	for _, mode := range []string{"cancel", "invalid-path", "missing-home", "loop-home", "canonical-mismatch", "planted-source", "directory", "probe-error", "closed-probe", "opened-unreadable"} {
		t.Run(mode, func(t *testing.T) {
			g, _, p, _ := realFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rel := "a"
			want := ErrSource
			switch mode {
			case "cancel":
				cancel()
			case "invalid-path":
				rel = "a\x7f"
				if e := os.WriteFile(filepath.Join(g.Home.LogicalPath, rel), nil, 0600); e != nil {
					t.Fatal(e)
				}
			case "missing-home":
				g.Home.LogicalPath = filepath.Join(t.TempDir(), "absent")
				want = ErrSourceAbsent
			case "loop-home":
				path := filepath.Join(t.TempDir(), "loop")
				if e := os.Symlink(path, path); e != nil {
					t.Fatal(e)
				}
				g.Home.LogicalPath = path
			case "canonical-mismatch":
				g.Home.CanonicalPath = filepath.Dir(g.Home.CanonicalPath)
				want = ErrSourceEscaped
			case "planted-source":
				g.Home.PlantedRoots = append(g.Home.PlantedRoots, filepath.Join(g.Home.CanonicalPath, "a"))
				want = ErrSourceEscaped
			case "directory":
				path := filepath.Join(g.Home.LogicalPath, "a")
				if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
				if e := os.Mkdir(path, 0700); e != nil {
					t.Fatal(e)
				}
				want = nil
			case "probe-error":
				p.openSource = func(*os.Root, string) (*os.File, error) { return nil, ErrSource }
			case "closed-probe":
				p.openSource = func(root *os.Root, rel string) (*os.File, error) {
					f, e := openSourceReadOnly(root, rel)
					if e != nil {
						return nil, e
					}
					if e = f.Close(); e != nil {
						t.Fatal(e)
					}
					return f, nil
				}
			case "opened-unreadable":
				p.openSource = func(root *os.Root, rel string) (*os.File, error) {
					f, e := openSourceReadOnly(root, rel)
					if e != nil {
						return nil, e
					}
					if e := os.Chmod(filepath.Join(g.Home.LogicalPath, rel), 0000); e != nil {
						t.Fatal(e)
					}
					return f, nil
				}
			}
			observed, e := p.Source(ctx, g.Home, rel)
			if want == nil {
				if e != nil || !observed.Accessible {
					t.Fatal(observed, e)
				}
				return
			}
			if !errors.Is(e, want) || observed.Accessible {
				t.Fatalf("got %+v, %v; want %v", observed, e, want)
			}
		})
	}
}
func TestDestinationObservationsRequireValidInputs(t *testing.T) {
	for _, mode := range []string{"cancel-before-candidate", "invalid-before-candidate", "unsafe-candidate"} {
		t.Run(mode, func(t *testing.T) {
			g, _, p, _ := realFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rel := "a"
			if mode == "unsafe-candidate" {
				if e := os.Chmod(g.Candidate.Path, 0777); e != nil {
					t.Fatal(e)
				}
			} else {
				if e := os.Remove(g.Candidate.Path); e != nil {
					t.Fatal(e)
				}
				if mode == "cancel-before-candidate" {
					cancel()
				} else {
					rel = "a\x7f"
				}
			}
			_, e := p.Destination(ctx, g.Candidate, rel)
			if e == nil {
				t.Fatal("invalid destination observation accepted")
			}
		})
	}
}
func TestCandidateOpenChecksMutationAndAllowedBases(t *testing.T) {
	for _, mode := range []string{"mutation-root", "allowed-root", "missing-base", "outside-base", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			g, _, p, _ := realFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "mutation-root":
				g.Candidate.MutationIdentity = g.Candidate.Path
			case "allowed-root":
				g.Candidate.AllowedBase = g.Candidate.Path
			case "missing-base":
				g.Candidate.AllowedBase = filepath.Join(t.TempDir(), "absent")
			case "outside-base":
				g.Candidate.AllowedBase = t.TempDir()
			case "cancel":
				cancel()
			}
			s, e := p.OpenCandidate(ctx, g.Candidate)
			if s != nil {
				defer s.Close()
			}
			if e == nil {
				t.Fatal("unproved candidate accepted")
			}
		})
	}
}
func TestCandidateValidationHonorsCancellation(t *testing.T) {
	g, _, p, _ := realFixture(t)
	s, e := p.OpenCandidate(context.Background(), g.Candidate)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := s.Validate(ctx); e == nil {
		t.Fatal("canceled validation accepted")
	}
}
func TestEveryDestinationParentMustBeSafe(t *testing.T) {
	for _, mode := range []string{"symlink-intermediate", "writable-intermediate"} {
		t.Run(mode, func(t *testing.T) {
			g, _, p, _ := realFixture(t)
			outer := filepath.Join(g.Candidate.Path, "outer")
			if e := os.MkdirAll(filepath.Join(outer, "inner"), 0700); e != nil {
				t.Fatal(e)
			}
			rel := "outer/inner/a"
			if mode == "symlink-intermediate" {
				if e := os.Symlink("outer", filepath.Join(g.Candidate.Path, "alias")); e != nil {
					t.Fatal(e)
				}
				rel = "alias/inner/a"
			} else {
				if e := os.Chmod(outer, 0777); e != nil {
					t.Fatal(e)
				}
			}
			s, e := p.OpenCandidate(context.Background(), g.Candidate)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if _, e := s.CreateExclusive(context.Background(), filepath.Join(g.Home.LogicalPath, "a"), rel); e == nil {
				t.Fatal("unsafe intermediate parent accepted")
			}
			if _, e := os.Lstat(filepath.Join(outer, "inner", "a")); !os.IsNotExist(e) {
				t.Fatal("entry created through unsafe parent", e)
			}
		})
	}
}
func TestClosedCandidateRejectsFurtherUse(t *testing.T) {
	g, _, p, _ := realFixture(t)
	s, e := p.OpenCandidate(context.Background(), g.Candidate)
	if e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	if e := s.Validate(context.Background()); e == nil {
		t.Fatal("closed session validated")
	}
	if _, e := s.CreateExclusive(context.Background(), filepath.Join(g.Home.LogicalPath, "a"), "a"); e == nil {
		t.Fatal("closed session created link")
	}
	if e := s.Close(); e != nil {
		t.Fatal("second close must be harmless", e)
	}
}
