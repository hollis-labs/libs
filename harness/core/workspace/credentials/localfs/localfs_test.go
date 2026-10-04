package localfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/credentials"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

type recordingSink struct {
	rows []effects.Evidence
	hook func(effects.Evidence)
}

func (s *recordingSink) Record(_ context.Context, e effects.Evidence) error {
	if s.hook != nil {
		s.hook(e)
	}
	s.rows = append(s.rows, e.Clone())
	return nil
}
func realFixture(t *testing.T) (credentials.Group, effects.ApplyContext, *Port, *recordingSink) {
	t.Helper()
	if !supportedOS(runtime.GOOS) {
		t.Skip("confined links require Linux or macOS")
	}
	base := t.TempDir()
	var err error
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "resources")
	identity := filepath.Join(base, "boot")
	candidate := filepath.Join(identity, "candidate")
	locks := filepath.Join(base, "locks")
	for _, p := range []string{home, identity, candidate, locks} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	i := credentials.CapturedProviderHome{Provider: "fixture", Path: home, AllowedBase: base, Provenance: "host", CaptureID: "capture", Revision: "revision", BeforeRedirect: true, PlantedRoots: []string{identity}}
	h, err := credentials.ResolveRealHome(i, credentials.HomeObservations{CanonicalHome: home, CanonicalBase: base, CanonicalPlantedRoots: []string{identity}})
	if err != nil {
		t.Fatal(err)
	}
	g := credentials.Group{Header: effects.Header{Version: effects.SchemaVersion, OperationID: "operation", InputDigest: "input"}, Layer: "boot", Home: h, Candidate: effects.RootInput{ID: "candidate", Path: candidate, AllowedBase: identity, MutationIdentity: identity, Owner: "fixture", Provenance: "host", Inactive: true, PrivateCustody: true}}
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("secret-sentinel"), 0600); err != nil {
			t.Fatal(err)
		}
		g.Bindings = append(g.Bindings, credentials.Binding{Source: name, Destination: name, Required: true, AuthorizationID: "grant", AuthorizationVersion: "version", SourceRead: true})
	}
	sink := &recordingSink{}
	c := effects.ApplyContext{PreflightContext: effects.PreflightContext{Header: g.Header, HeldLocks: []effects.LockIdentity{{Namespace: locks, CanonicalID: identity}}, Validate: func(context.Context) error { return nil }}, ArtifactRootID: g.Candidate.ID, ArtifactGeneration: "generation", Receipts: sink}
	return g, c, New(), sink
}
func realApply(t *testing.T, g credentials.Group, c effects.ApplyContext, p credentials.LinkPort) effects.Result {
	t.Helper()
	prepared, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
	if r.Code != "preflight_complete" {
		t.Fatalf("preflight failed: %+v", r)
	}
	return credentials.Apply(context.Background(), prepared, c, p)
}

func TestRealLinksIdempotenceAtomicReauthAndNoLeak(t *testing.T) {
	g, c, p, s := realFixture(t)
	r := realApply(t, g, c, p)
	if r.Outcome != effects.Applied {
		t.Fatal(r)
	}
	for _, b := range g.Bindings {
		want := filepath.Join(g.Home.LogicalPath, b.Source)
		got, e := os.Readlink(filepath.Join(g.Candidate.Path, b.Destination))
		if e != nil || got != want {
			t.Fatalf("incorrect logical link: %s %v", got, e)
		}
	}
	before, e := os.Lstat(filepath.Join(g.Candidate.Path, "a"))
	if e != nil {
		t.Fatal(e)
	}
	r2 := realApply(t, g, c, p)
	after, e := os.Lstat(filepath.Join(g.Candidate.Path, "a"))
	if e != nil || r2.Outcome != effects.AlreadyPresent || !os.SameFile(before, after) {
		t.Fatal("idempotence changed link")
	}
	newSource := filepath.Join(g.Home.LogicalPath, "replacement")
	if err := os.WriteFile(newSource, []byte("new-secret-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newSource, filepath.Join(g.Home.LogicalPath, "a")); err != nil {
		t.Fatal(err)
	}
	// Only the test reads the dummy fixture: production never reads credentials.
	bytes, e := os.ReadFile(filepath.Join(g.Candidate.Path, "a"))
	if e != nil || string(bytes) != "new-secret-sentinel" {
		t.Fatal("reauth replacement not visible")
	}
	r3 := credentials.Inspect(context.Background(), g, r.Evidence, c.PreflightContext, p)
	if r3.Code != "inspected_complete" {
		t.Fatal(r3)
	}
	if strings.Contains(fmt.Sprintf("%+v %+v", r, s.rows), "secret-sentinel") {
		t.Fatal("credential content leaked into evidence")
	}
}

func TestRealPreflightEverySourceAndDestination(t *testing.T) {
	for _, kind := range []string{"missing", "unreadable", "file", "link", "directory", "escape", "special"} {
		t.Run(kind, func(t *testing.T) {
			g, c, p, _ := realFixture(t)
			source := filepath.Join(g.Home.LogicalPath, "c")
			dest := filepath.Join(g.Candidate.Path, "c")
			switch kind {
			case "missing":
				os.Remove(source)
			case "unreadable":
				os.Chmod(source, 0000)
				defer os.Chmod(source, 0600)
			case "file":
				os.WriteFile(dest, []byte("operator"), 0600)
			case "link":
				os.Symlink(filepath.Join(g.Home.LogicalPath, "a"), dest)
			case "directory":
				os.Mkdir(dest, 0700)
			case "escape":
				os.Remove(source)
				os.Symlink(filepath.Join(g.Candidate.Path, "a"), source)
			case "special":
				os.Remove(source)
				os.Symlink("/dev/null", source)
			}
			_, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
			if r.Code == "preflight_complete" {
				t.Fatal("late failure missed")
			}
			for _, name := range []string{"a", "b"} {
				if _, e := os.Lstat(filepath.Join(g.Candidate.Path, name)); !os.IsNotExist(e) {
					t.Fatal("preflight mutated candidate")
				}
			}
		})
	}
}

func TestRealDestinationParentsAndFutureCandidate(t *testing.T) {
	for _, kind := range []string{"symlink-outside", "symlink-inside", "parent-file", "missing-parent", "missing-candidate"} {
		t.Run(kind, func(t *testing.T) {
			g, c, p, _ := realFixture(t)
			g.Bindings[0].Destination = "nested/a"
			parent := filepath.Join(g.Candidate.Path, "nested")
			switch kind {
			case "symlink-outside":
				os.Symlink(g.Home.LogicalPath, parent)
			case "symlink-inside":
				os.Mkdir(filepath.Join(g.Candidate.Path, "inside"), 0700)
				os.Symlink("inside", parent)
			case "parent-file":
				os.WriteFile(parent, []byte("body"), 0600)
			case "missing-candidate":
				os.Remove(g.Candidate.Path)
			}
			prepared, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
			if kind == "missing-candidate" || kind == "missing-parent" {
				if r.Code != "preflight_complete" {
					t.Fatalf("future parents should be observed only: %+v", r)
				}
				x := credentials.Apply(context.Background(), prepared, c, p)
				if x.Code == "group_complete" {
					t.Fatal("missing actual parent accepted")
				}
			} else if r.Code == "preflight_complete" {
				t.Fatal("unsafe parent accepted")
			}
		})
	}
}

func TestRealCustodyGuards(t *testing.T) {
	for _, kind := range []string{"root-link", "root-mode", "active", "custody", "parent-swapped", "root-swapped", "link-swapped"} {
		t.Run(kind, func(t *testing.T) {
			g, _, p, _ := realFixture(t)
			if kind == "root-mode" {
				os.Chmod(g.Candidate.Path, 0755)
			}
			if kind == "root-link" {
				os.Rename(g.Candidate.Path, g.Candidate.Path+"-old")
				os.Symlink(g.Candidate.Path+"-old", g.Candidate.Path)
			}
			if kind == "active" {
				g.Candidate.Inactive = false
			}
			if kind == "custody" {
				g.Candidate.PrivateCustody = false
			}
			s, e := p.OpenCandidate(context.Background(), g.Candidate)
			if kind == "root-link" || kind == "root-mode" || kind == "active" || kind == "custody" {
				if e == nil {
					s.Close()
					t.Fatal("unsafe root accepted")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if kind == "parent-swapped" {
				os.Mkdir(filepath.Join(g.Candidate.Path, "nested"), 0700)
				if _, e := s.Inspect(context.Background(), "nested/a"); e != nil {
					t.Fatal(e)
				}
				os.Rename(filepath.Join(g.Candidate.Path, "nested"), filepath.Join(g.Candidate.Path, "old"))
				os.Mkdir(filepath.Join(g.Candidate.Path, "nested"), 0700)
				if _, e := s.CreateExclusive(context.Background(), filepath.Join(g.Home.LogicalPath, "a"), "nested/a"); e == nil {
					t.Fatal("swapped pinned parent accepted")
				}
				return
			}
			o, e := s.CreateExclusive(context.Background(), filepath.Join(g.Home.LogicalPath, "a"), "a")
			if e != nil {
				t.Fatal(e)
			}
			if kind == "root-swapped" {
				os.Rename(g.Candidate.Path, g.Candidate.Path+"-old")
				os.Mkdir(g.Candidate.Path, 0700)
			} else {
				os.Rename(filepath.Join(g.Candidate.Path, "a"), filepath.Join(g.Candidate.Path, "old-a"))
				os.Symlink(filepath.Join(g.Home.LogicalPath, "b"), filepath.Join(g.Candidate.Path, "a"))
			}
			if e := s.RemoveIfMatches(context.Background(), "a", o); e == nil {
				t.Fatal("swapped resource removed")
			}
		})
	}
}

type failingPort struct {
	credentials.LinkPort
	position, calls int
	uncertain       bool
}

func (p *failingPort) OpenCandidate(ctx context.Context, r effects.RootInput) (credentials.CandidateSession, error) {
	s, e := p.LinkPort.OpenCandidate(ctx, r)
	if e != nil {
		return nil, e
	}
	return &failingSession{CandidateSession: s, port: p}, nil
}

type failingSession struct {
	credentials.CandidateSession
	port *failingPort
}

func (s *failingSession) CreateExclusive(ctx context.Context, source, dest string) (credentials.LinkObservation, error) {
	s.port.calls++
	if s.port.calls == s.port.position {
		if s.port.uncertain {
			o, e := s.CandidateSession.CreateExclusive(ctx, source, dest)
			if e != nil {
				return o, e
			}
			return o, ErrDestination
		}
		return credentials.LinkObservation{}, ErrDestination
	}
	return s.CandidateSession.CreateExclusive(ctx, source, dest)
}

func TestRealFailureEveryLinkRollsBackOnlyCreated(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		for n := 1; n <= 3; n++ {
			t.Run(fmt.Sprintf("%t/%d", uncertain, n), func(t *testing.T) {
				g, c, p, _ := realFixture(t)
				f := &failingPort{LinkPort: p, position: n, uncertain: uncertain}
				r := realApply(t, g, c, f)
				if n > 1 || uncertain {
					if r.Outcome != effects.Partial {
						t.Fatal(r)
					}
				} else if r.Outcome != effects.Conflict {
					t.Fatal(r)
				}
				for _, b := range g.Bindings {
					if _, e := os.Lstat(filepath.Join(g.Candidate.Path, b.Destination)); !os.IsNotExist(e) {
						t.Fatalf("created link retained after safe rollback: %v", e)
					}
					if _, e := os.Stat(filepath.Join(g.Home.LogicalPath, b.Source)); e != nil {
						t.Fatal("source touched")
					}
				}
			})
		}
	}
}

func TestRealSwapBetweenPreflightAndCreateNeverOverwrites(t *testing.T) {
	g, c, p, s := realFixture(t)
	f := &failingPort{LinkPort: p, position: 99}
	s.hook = func(e effects.Evidence) {
		if e.Phase == "link_created" && f.calls == 1 {
			os.WriteFile(filepath.Join(g.Candidate.Path, "b"), []byte("operator"), 0600)
		}
	}
	r := realApply(t, g, c, f)
	if r.Outcome != effects.Partial {
		t.Fatal(r)
	}
	bytes, e := os.ReadFile(filepath.Join(g.Candidate.Path, "b"))
	if e != nil || string(bytes) != "operator" {
		t.Fatal("concurrent user file overwritten")
	}
	if _, e := os.Lstat(filepath.Join(g.Candidate.Path, "a")); !os.IsNotExist(e) {
		t.Fatal("first created link not compensated")
	}
}

func TestUnsupportedPlatformStubOnHost(t *testing.T) {
	for _, platform := range []string{"windows", "plan9", "js", "wasip1", "freebsd"} {
		if supportedOS(platform) {
			t.Fatalf("unsupported platform enabled: %s", platform)
		}
		p := &Port{platform: platform}
		if p.Supported() {
			t.Fatal("unsupported port enabled")
		}
		if _, err := p.Source(context.Background(), credentials.ResolvedHome{}, "a"); err != ErrUnsupported {
			t.Fatal("source stub did not refuse")
		}
		if _, err := p.Destination(context.Background(), effects.RootInput{}, "a"); err != ErrUnsupported {
			t.Fatal("destination stub did not refuse")
		}
		if _, err := p.OpenCandidate(context.Background(), effects.RootInput{}); err != ErrUnsupported {
			t.Fatal("candidate stub did not refuse")
		}
	}
}
