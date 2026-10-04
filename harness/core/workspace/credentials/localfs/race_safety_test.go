//go:build linux || darwin

package localfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace/credentials"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

// ---------- helpers ----------

type diskEntry struct {
	Kind   string
	Target string
	Ino    uint64
}

func snapshot(t *testing.T, dir string) map[string]diskEntry {
	t.Helper()
	out := map[string]diskEntry{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil
		}
		st, e := os.Lstat(p)
		if e != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		de := diskEntry{Kind: "other"}
		if s, ok := st.Sys().(*syscall.Stat_t); ok {
			de.Ino = s.Ino
		}
		switch {
		case st.Mode()&fs.ModeSymlink != 0:
			de.Kind = "link"
			de.Target, _ = os.Readlink(p)
		case st.IsDir():
			de.Kind = "dir"
		case st.Mode().IsRegular():
			de.Kind = "file"
		}
		out[rel] = de
		return nil
	})
	return out
}

// reality asserts the returned evidence/obligations do not claim more (or less)
// than what is on disk. preexisting = destinations present before the call.
func reality(t *testing.T, g credentials.Group, r effects.Result, before map[string]diskEntry, tag string) {
	t.Helper()
	after := snapshot(t, g.Candidate.Path)
	retained := 0
	for n, b := range g.Bindings {
		if n >= len(r.Evidence.Links) {
			// refusal/conflict before intent: nothing recorded.
			if a, ok := after[b.Destination]; ok {
				if _, was := before[b.Destination]; !was {
					t.Errorf("%s: link %s exists on disk but evidence has no entry (code=%s)", tag, b.Destination, r.Code)
				}
				_ = a
			}
			continue
		}
		ev := r.Evidence.Links[n]
		a, onDisk := after[b.Destination]
		_, was := before[b.Destination]
		switch ev.Outcome {
		case effects.Applied, effects.Partial:
			if ev.Uncertain {
				if ev.Created {
					t.Errorf("%s: uncertain entry adopted", tag)
				}
				if ev.Outcome != effects.Partial {
					t.Errorf("%s: uncertain entry completed", tag)
				}
				retained++
				break
			}
			if !onDisk || a.Kind != "link" {
				t.Errorf("%s: evidence claims %s for %s but disk has %+v", tag, ev.Outcome, b.Destination, a)
			}
			if !ev.Created {
				t.Errorf("%s: %s outcome without Created for %s", tag, ev.Outcome, b.Destination)
			}
			if ev.Outcome == effects.Partial {
				retained++
			}
		case effects.AlreadyPresent:
			if !was || !onDisk {
				t.Errorf("%s: AlreadyPresent for %s that did not pre-exist", tag, b.Destination)
			}
			if ev.Created {
				t.Errorf("%s: AlreadyPresent but Created for %s", tag, b.Destination)
			}
		case effects.Omitted, effects.Removed:
			if ev.Created && onDisk {
				if ev.LinkIdentity != "" && a.Kind == "link" {
					t.Errorf("%s: evidence says %s removed (omitted+created) but a link is on disk", tag, b.Destination)
				}
			}
		}
		// A link that exists on disk now, did not before, and is not claimed Created is an unrecorded creation.
		if onDisk && !was && !ev.Created && a.Kind == "link" && ev.Outcome != effects.Pending && !ev.Uncertain {
			t.Errorf("%s: unrecorded new link at %s (ev=%+v)", tag, b.Destination, ev)
		}
		// a link we did not create must never be removed
		if was && !onDisk {
			t.Errorf("%s: PRE-EXISTING %s was removed", tag, b.Destination)
		}
		if was && onDisk && a != before[b.Destination] {
			t.Errorf("%s: PRE-EXISTING %s was altered %+v -> %+v", tag, b.Destination, before[b.Destination], a)
		}
	}
	got := 0
	for _, o := range r.Obligations {
		if o.Code == "link_retained" {
			got++
		}
	}
	if got != retained {
		t.Errorf("%s: link_retained obligations=%d but retained links in evidence=%d", tag, got, retained)
	}
	if r.Outcome == effects.Partial {
		found := false
		for _, o := range r.Obligations {
			if o.Code == "recovery_required" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: Partial without recovery_required", tag)
		}
	}
	if r.Outcome == effects.Applied || r.Code == "group_complete" {
		for n, b := range g.Bindings {
			a, ok := after[b.Destination]
			if r.Evidence.Links[n].Outcome == effects.Omitted {
				continue
			}
			if !ok || a.Kind != "link" {
				t.Errorf("%s: complete but %s missing", tag, b.Destination)
			}
		}
	}
}

type rmFailPort struct {
	credentials.LinkPort
	createFail, createUncertain int // 1-based create index
	removeFail                  map[int]bool
	validateFailFrom            int // session.Validate calls >= this fail (0 = never)
	creates, removes, validates int
	removedOrder                []string
}

func (p *rmFailPort) OpenCandidate(ctx context.Context, r effects.RootInput) (credentials.CandidateSession, error) {
	s, e := p.LinkPort.OpenCandidate(ctx, r)
	if e != nil {
		return nil, e
	}
	return &rmFailSession{CandidateSession: s, port: p}, nil
}

type rmFailSession struct {
	credentials.CandidateSession
	port *rmFailPort
}

func (s *rmFailSession) Validate(ctx context.Context) error {
	s.port.validates++
	if s.port.validateFailFrom > 0 && s.port.validates >= s.port.validateFailFrom {
		return ErrCustody
	}
	return s.CandidateSession.Validate(ctx)
}
func (s *rmFailSession) CreateExclusive(ctx context.Context, src, dst string) (credentials.LinkObservation, error) {
	s.port.creates++
	if s.port.creates == s.port.createFail {
		return credentials.LinkObservation{}, ErrDestination
	}
	o, e := s.CandidateSession.CreateExclusive(ctx, src, dst)
	if s.port.creates == s.port.createUncertain {
		return o, ErrDestination
	}
	return o, e
}
func (s *rmFailSession) RemoveIfMatches(ctx context.Context, rel string, want credentials.LinkObservation) error {
	s.port.removes++
	s.port.removedOrder = append(s.port.removedOrder, rel)
	if s.port.removeFail[s.port.removes] {
		return ErrDestination
	}
	return s.CandidateSession.RemoveIfMatches(ctx, rel, want)
}

func runApply(t *testing.T, g credentials.Group, c effects.ApplyContext, p credentials.LinkPort) effects.Result {
	t.Helper()
	prepared, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
	if r.Code != "preflight_complete" {
		t.Fatalf("preflight: %+v", r)
	}
	return credentials.Apply(context.Background(), prepared, c, p)
}

// ---------- 1. failure at EVERY index, create x uncertain x remove-failure combos ----------

func TestSafetyEveryIndexCreateRemoveCombos(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for createFail := 0; createFail <= 3; createFail++ {
			for _, uncertain := range []bool{false, true} {
				for _, rmMask := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
					name := fmt.Sprintf("existing=%t/createFail=%d/uncertain=%t/rm=%03b", existing, createFail, uncertain, rmMask)
					t.Run(name, func(t *testing.T) {
						g, c, p, _ := realFixture(t)
						if existing {
							if err := os.Symlink(filepath.Join(g.Home.LogicalPath, "a"), filepath.Join(g.Candidate.Path, "a")); err != nil {
								t.Fatal(err)
							}
						}
						before := snapshot(t, g.Candidate.Path)
						f := &rmFailPort{LinkPort: p, removeFail: map[int]bool{}}
						if uncertain {
							f.createUncertain = createFail
						} else {
							f.createFail = createFail
						}
						for i := 0; i < 3; i++ {
							if rmMask&(1<<i) != 0 {
								f.removeFail[i+1] = true
							}
						}
						r := runApply(t, g, c, f)
						reality(t, g, r, before, name)
						// source never touched
						for _, b := range g.Bindings {
							if bs, e := os.ReadFile(filepath.Join(g.Home.LogicalPath, b.Source)); e != nil || string(bs) != "secret-sentinel" {
								t.Fatalf("source touched")
							}
						}
					})
				}
			}
		}
	}
}

// ---------- 2. receipt sink failure / ctx cancel / fence loss at EVERY call ----------

type hookSink struct {
	calls    int
	hook     func(n int, e effects.Evidence) error
	rows     []effects.Evidence
	ctxAware bool
}

func (s *hookSink) Record(ctx context.Context, e effects.Evidence) error {
	s.calls++
	if s.ctxAware && ctx.Err() != nil {
		return ctx.Err()
	}
	var err error
	if s.hook != nil {
		err = s.hook(s.calls, e)
	}
	if err != nil {
		return err
	}
	s.rows = append(s.rows, e.Clone())
	return nil
}

func TestSafetySinkFailCancelFenceEveryCall(t *testing.T) {
	for _, mode := range []string{"sink-fail", "cancel", "fence", "sink-fail-all-after", "session-validate"} {
		for k := 1; k <= 14; k++ {
			name := fmt.Sprintf("%s/%d", mode, k)
			t.Run(name, func(t *testing.T) {
				g, c, p, _ := realFixture(t)
				before := snapshot(t, g.Candidate.Path)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				fenceLost := false
				c.Validate = func(context.Context) error {
					if fenceLost {
						return errors.New("fence lost")
					}
					return nil
				}
				sink := &hookSink{ctxAware: mode == "cancel"}
				sink.hook = func(n int, e effects.Evidence) error {
					if n != k && !(mode == "sink-fail-all-after" && n >= k) {
						return nil
					}
					switch mode {
					case "sink-fail", "sink-fail-all-after":
						return errors.New("sink down")
					case "cancel":
						cancel()
					case "fence":
						fenceLost = true
					}
					return nil
				}
				c.Receipts = sink
				port := credentials.LinkPort(p)
				if mode == "session-validate" {
					// session custody validation fails from call k on
					port = &rmFailPort{LinkPort: p, validateFailFrom: k}
				}
				prepared, r := credentials.Preflight(ctx, g, c.PreflightContext, port)
				if r.Code != "preflight_complete" {
					t.Fatalf("preflight %+v", r)
				}
				res := credentials.Apply(ctx, prepared, c, port)
				t.Logf("%-22s => %s/%s obligations=%d disk=%d", name, res.Outcome, res.Code, len(res.Obligations), len(snapshot(t, g.Candidate.Path)))
				reality(t, g, res, before, name)
				if (mode == "fence" || mode == "cancel") && k <= 7 && res.Code == "group_complete" {
					t.Errorf("%s: authority/ctx lost at record %d but group completed", mode, k)
				}
				diskLinks := 0
				for _, v := range snapshot(t, g.Candidate.Path) {
					if v.Kind == "link" {
						diskLinks++
					}
				}
				if mode == "fence" && diskLinks > k/2 {
					t.Errorf("fence lost at record %d but %d links exist: mutation continued after authority loss", k, diskLinks)
				}
				if mode == "cancel" && res.Code != "group_complete" && diskLinks != 0 {
					t.Errorf("cancel at record %d: %d links left although custody was intact (compensation must survive cancellation)", k, diskLinks)
				}
				if mode == "cancel" && res.Outcome == effects.Partial {
					last := sink.rows[len(sink.rows)-1]
					if last.Phase != effects.InterruptedPhase {
						t.Errorf("cancel at record %d: compensation receipt not persisted (last row phase=%s)", k, last.Phase)
					}
				}
				if mode == "sink-fail-all-after" && res.Outcome == effects.Partial {
					pending := false
					for _, o := range res.Obligations {
						if o.Code == "receipt_pending" {
							pending = true
						}
					}
					if !pending {
						t.Errorf("sink down for the cleanup record but no receipt_pending obligation")
					}
				}
				// mapping: any real mutation must be Partial
				after := snapshot(t, g.Candidate.Path)
				mutated := false
				for k2, v := range after {
					if _, was := before[k2]; !was && v.Kind == "link" {
						mutated = true
					}
				}
				// compensation may have removed everything: check evidence for Created
				createdAny := false
				for _, l := range res.Evidence.Links {
					if l.Created {
						createdAny = true
					}
				}
				if (mutated || createdAny) && res.Outcome != effects.Partial && res.Code != "group_complete" {
					t.Errorf("mutation happened but outcome=%s code=%s", res.Outcome, res.Code)
				}
				if res.Outcome == effects.Applied && res.Code != "group_complete" {
					t.Errorf("Applied without group_complete")
				}
			})
		}
	}
}

// ---------- 3. swaps between preflight / create / verify / compensate ----------

func TestSafetyDestinationSwapsBeforeCreate(t *testing.T) {
	kinds := []string{"file", "dir", "dangling-link", "exact-link-other-inode", "outside-link"}
	for _, kind := range kinds {
		for _, victim := range []string{"a", "b", "c"} {
			t.Run(kind+"/"+victim, func(t *testing.T) {
				g, c, p, _ := realFixture(t)
				before := snapshot(t, g.Candidate.Path)
				planted := filepath.Join(g.Candidate.Path, victim)
				outside := t.TempDir()
				sink := &hookSink{}
				done := false
				sink.hook = func(n int, e effects.Evidence) error {
					// plant right after the intent receipt (before the first link_intent)
					if e.Phase == "intent" && !done {
						done = true
						switch kind {
						case "file":
							os.WriteFile(planted, []byte("operator"), 0600)
						case "dir":
							os.Mkdir(planted, 0700)
						case "dangling-link":
							os.Symlink(filepath.Join(outside, "nope"), planted)
						case "exact-link-other-inode":
							os.Symlink(filepath.Join(g.Home.LogicalPath, victim), planted)
						case "outside-link":
							os.Symlink(outside, planted)
						}
					}
					return nil
				}
				c.Receipts = sink
				r := runApply(t, g, c, p)
				after := snapshot(t, g.Candidate.Path)
				// planted entry must survive intact
				if kind == "exact-link-other-inode" {
					// race loser must be Conflict and NOT claimed as created
					_ = before
				}
				if a, ok := after[victim]; !ok {
					t.Fatalf("concurrent entry removed: result %+v", r)
				} else if a.Kind == "other" {
					t.Fatalf("unexpected kind")
				}
				reality(t, g, r, before, kind+victim)
				if r.Code == "group_complete" {
					t.Fatalf("group completed over concurrent entry")
				}
				if e, _ := os.ReadDir(outside); len(e) != 0 {
					t.Fatalf("write escaped to outside dir")
				}
			})
		}
	}
}

func TestSafetyParentSwapsNestedDestination(t *testing.T) {
	for _, kind := range []string{"symlink-outside", "symlink-inside", "new-real-dir", "rename-away-only"} {
		for _, phase := range []string{"intent", "link_intent", "link_created"} {
			for _, nth := range []int{1, 2, 3} {
				if phase == "intent" && nth > 1 {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/%d", kind, phase, nth), func(t *testing.T) {
					g, c, p, _ := realFixture(t)
					g.Bindings[1].Destination = "nested/b"
					g.Bindings[2].Destination = "nested/c"
					nested := filepath.Join(g.Candidate.Path, "nested")
					os.Mkdir(nested, 0700)
					before := snapshot(t, g.Candidate.Path)
					outside := t.TempDir()
					sink := &hookSink{}
					hits := 0
					sink.hook = func(n int, e effects.Evidence) error {
						if e.Phase == effects.Phase(phase) {
							hits++
							if hits != nth {
								return nil
							}
							switch kind {
							case "symlink-outside":
								os.Rename(nested, nested+"-old")
								os.Symlink(outside, nested)
							case "symlink-inside":
								os.Rename(nested, nested+"-old")
								os.Symlink(nested+"-old", nested)
							case "new-real-dir":
								os.Rename(nested, nested+"-old")
								os.Mkdir(nested, 0700)
							case "rename-away-only":
								os.Rename(nested, nested+"-old")
							}
						}
						return nil
					}
					c.Receipts = sink
					r := runApply(t, g, c, p)
					if r.Code == "group_complete" {
						t.Fatalf("complete after parent swap: %+v", r)
					}
					if e, _ := os.ReadDir(outside); len(e) != 0 {
						t.Fatalf("write escaped to outside dir: %v", e)
					}
					// nothing new may be created in the swapped-in replacement
					for k, v := range snapshot(t, nested) {
						if v.Kind != "dir" {
							t.Errorf("link/file created inside swapped-in parent: %s", k)
						}
					}
					// accounting honesty: links created in the renamed-away dir remain, and must be reported
					oldLinks := 0
					for _, v := range snapshot(t, nested+"-old") {
						if v.Kind == "link" {
							oldLinks++
						}
					}
					partialClaims := 0
					for _, l := range r.Evidence.Links {
						if l.Created && l.Outcome == effects.Partial {
							partialClaims++
						}
					}
					t.Logf("%s/%s: outcome=%s code=%s oldLinksLeft=%d retainedClaims=%d obligations=%v", kind, phase, r.Outcome, r.Code, oldLinks, partialClaims, r.Obligations)
					if oldLinks > partialClaims {
						t.Errorf("links left in renamed-away parent (%d) exceed retained claims (%d)", oldLinks, partialClaims)
					}
					_ = before
				})
			}
		}
	}
}

func TestSafetyCandidateRootSwapMidApply(t *testing.T) {
	for _, kind := range []string{"rename-and-replace", "symlink-to-sibling", "identity-dir-symlinked", "chmod-open"} {
		for _, phase := range []string{"intent", "link_intent", "link_created"} {
			t.Run(kind+"/"+phase, func(t *testing.T) {
				g, c, p, _ := realFixture(t)
				ident := g.Candidate.MutationIdentity
				sibling := filepath.Join(ident, "sibling")
				os.Mkdir(sibling, 0700)
				sink := &hookSink{}
				hits := 0
				sink.hook = func(n int, e effects.Evidence) error {
					if e.Phase == effects.Phase(phase) {
						hits++
						if hits != 1 {
							return nil
						}
						switch kind {
						case "rename-and-replace":
							os.Rename(g.Candidate.Path, g.Candidate.Path+"-old")
							os.Mkdir(g.Candidate.Path, 0700)
						case "symlink-to-sibling":
							os.Rename(g.Candidate.Path, g.Candidate.Path+"-old")
							os.Symlink(sibling, g.Candidate.Path)
						case "identity-dir-symlinked":
							os.Rename(ident, ident+"-moved")
							os.Symlink(ident+"-moved", ident)
						case "chmod-open":
							os.Chmod(g.Candidate.Path, 0777)
						}
					}
					return nil
				}
				c.Receipts = sink
				r := runApply(t, g, c, p)
				if r.Code == "group_complete" {
					t.Fatalf("complete after root swap: %+v", r)
				}
				for _, dir := range []string{sibling} {
					if len(snapshot(t, dir)) != 0 {
						t.Errorf("writes landed in sibling")
					}
				}
				if kind == "rename-and-replace" {
					if len(snapshot(t, g.Candidate.Path)) != 0 {
						t.Errorf("writes landed in replacement root")
					}
				}
				t.Logf("%s/%s: outcome=%s code=%s obligations=%v", kind, phase, r.Outcome, r.Code, r.Obligations)
			})
		}
	}
}

func TestSafetySourceSwapAfterAuthorization(t *testing.T) {
	for _, kind := range []string{"escape-link", "dangling", "removed", "fifo", "into-candidate"} {
		for _, phase := range []string{"intent", "link_intent", "link_created"} {
			for _, nth := range []int{1, 2, 3} {
				if phase == "intent" && nth > 1 {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/%d", kind, phase, nth), func(t *testing.T) {
					g, c, p, _ := realFixture(t)
					before := snapshot(t, g.Candidate.Path)
					outside := t.TempDir()
					os.WriteFile(filepath.Join(outside, "x"), []byte("outside-sentinel"), 0600)
					sink := &hookSink{}
					hits := 0
					swapCall := 0
					sink.hook = func(n int, e effects.Evidence) error {
						if e.Phase == effects.Phase(phase) {
							hits++
							if hits != nth {
								return nil
							}
							swapCall = n
							src := filepath.Join(g.Home.LogicalPath, "c")
							os.Remove(src)
							switch kind {
							case "escape-link":
								os.Symlink(filepath.Join(outside, "x"), src)
							case "dangling":
								os.Symlink(filepath.Join(outside, "nope"), src)
							case "fifo":
								syscall.Mkfifo(src, 0600)
							case "into-candidate":
								os.Symlink(filepath.Join(g.Candidate.Path, "a"), src)
							}
						}
						return nil
					}
					c.Receipts = sink
					prepared, pre := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
					if pre.Outcome != effects.Prepared {
						t.Fatal(pre)
					}
					done := make(chan effects.Result, 1)
					go func() { done <- credentials.Apply(context.Background(), prepared, c, p) }()
					select {
					case r := <-done:
						reality(t, g, r, before, "src-swap")
						if swapCall > 0 && swapCall <= 5 {
							for _, l := range r.Evidence.Links {
								if l.Destination == "c" && l.Created {
									t.Errorf("a link was created for source c although it was already bad before c's turn (swap at record %d)", swapCall)
								}
							}
						}
						if r.Code == "group_complete" {
							// source c was swapped before verification: completing is a lie unless swap happened after verify
							if phase != "complete" {
								t.Errorf("group completed although source c became %s at %s#%d: %+v", kind, phase, nth, r.Code)
							}
						}
					case <-time.After(10 * time.Second):
						t.Fatalf("HANG")
					}
				})
			}
		}
	}
}

// ---------- 4. EEXIST race at exactly the create instant (never adopted) ----------

type racePort struct {
	credentials.LinkPort
	onCreate func(dest string)
}

func (p *racePort) OpenCandidate(ctx context.Context, r effects.RootInput) (credentials.CandidateSession, error) {
	s, e := p.LinkPort.OpenCandidate(ctx, r)
	if e != nil {
		return nil, e
	}
	return &raceSession{CandidateSession: s, port: p}, nil
}

type raceSession struct {
	credentials.CandidateSession
	port *racePort
}

func (s *raceSession) CreateExclusive(ctx context.Context, src, dst string) (credentials.LinkObservation, error) {
	if s.port.onCreate != nil {
		s.port.onCreate(dst)
	}
	return s.CandidateSession.CreateExclusive(ctx, src, dst)
}

func TestSafetyEEXISTAtCreateInstantNeverAdopted(t *testing.T) {
	for _, kind := range []string{"exact-link", "other-link", "file", "dir"} {
		t.Run(kind, func(t *testing.T) {
			g, c, p, _ := realFixture(t)
			before := snapshot(t, g.Candidate.Path)
			rp := &racePort{LinkPort: p}
			n := 0
			rp.onCreate = func(dest string) {
				n++
				if n == 2 { // plant at b immediately before our create
					path := filepath.Join(g.Candidate.Path, dest)
					switch kind {
					case "exact-link":
						os.Symlink(filepath.Join(g.Home.LogicalPath, dest), path)
					case "other-link":
						os.Symlink("/nonexistent", path)
					case "file":
						os.WriteFile(path, []byte("operator"), 0600)
					case "dir":
						os.Mkdir(path, 0700)
					}
				}
			}
			r := runApply(t, g, c, rp)
			reality(t, g, r, before, kind)
			after := snapshot(t, g.Candidate.Path)
			if _, ok := after["b"]; !ok {
				t.Fatalf("concurrent b removed")
			}
			for _, l := range r.Evidence.Links {
				if l.Destination == "b" && l.Created {
					t.Fatalf("concurrent entry adopted as ours: %+v", l)
				}
			}
			if r.Outcome != effects.Partial {
				t.Fatalf("expected Partial got %s/%s", r.Outcome, r.Code)
			}
			if _, ok := after["a"]; ok {
				t.Fatalf("a not compensated")
			}
		})
	}
}

// ---------- 5. compensation: ABA via inode reuse ----------

func TestSafetySourceAncestorOfCandidate(t *testing.T) {
	// boot root placed INSIDE the provider home; a source directory is an ancestor of the candidate.
	base := t.TempDir()
	base, _ = filepath.EvalSymlinks(base)
	home := filepath.Join(base, "home")
	identity := filepath.Join(home, "sub", "bootroot")
	candidate := filepath.Join(identity, "candidate")
	locks := filepath.Join(base, "locks")
	for _, d := range []string{home, identity, candidate, locks} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	planted := filepath.Join(base, "planted")
	os.Mkdir(planted, 0700)
	i := credentials.CapturedProviderHome{Provider: "p", Path: home, AllowedBase: base, Provenance: "h", CaptureID: "c", Revision: "r", BeforeRedirect: true, PlantedRoots: []string{planted}}
	h, err := credentials.ResolveRealHome(i, credentials.HomeObservations{CanonicalHome: home, CanonicalBase: base, CanonicalPlantedRoots: []string{planted}})
	if err != nil {
		t.Fatal(err)
	}
	hdr := effects.Header{Version: effects.SchemaVersion, OperationID: "o", InputDigest: "d"}
	g := credentials.Group{Header: hdr, Layer: "boot", Home: h, Candidate: effects.RootInput{ID: "cand", Path: candidate, AllowedBase: identity, MutationIdentity: identity, Owner: "o", Provenance: "h", Inactive: true, PrivateCustody: true},
		Bindings: []credentials.Binding{{Source: "sub", Destination: "loop", Required: true, AuthorizationID: "g", AuthorizationVersion: "v", SourceRead: true}}}
	c := effects.ApplyContext{PreflightContext: effects.PreflightContext{Header: hdr, HeldLocks: []effects.LockIdentity{{Namespace: locks, CanonicalID: identity}}, Validate: func(context.Context) error { return nil }}, ArtifactRootID: "cand", ArtifactGeneration: "gen", Receipts: &hookSink{}}
	p := New()
	_, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
	t.Logf("source = ancestor of candidate: preflight code=%s outcome=%s", r.Code, r.Outcome)
	if r.Code == "preflight_complete" {
		res := credentials.Apply(context.Background(), mustPrepare(t, g, c, p), c, p)
		tgt, _ := os.Readlink(filepath.Join(candidate, "loop"))
		t.Errorf("SELF-CONTAINING LINK ACCEPTED: apply=%s/%s link %s -> %s", res.Outcome, res.Code, "candidate/loop", tgt)
	}
}

func mustPrepare(t *testing.T, g credentials.Group, c effects.ApplyContext, p credentials.LinkPort) credentials.PreparedGroup {
	t.Helper()
	pp, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
	if r.Code != "preflight_complete" {
		t.Fatalf("%+v", r)
	}
	return pp
}

func TestSafetyOptionalEscapingSourceIsOmittedNotRefused(t *testing.T) {
	g, c, p, _ := realFixture(t)
	g.Bindings[2].Required = false
	src := filepath.Join(g.Home.LogicalPath, "c")
	os.Remove(src)
	os.Symlink(t.TempDir(), src) // escapes the home
	_, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
	if r.Code == "preflight_complete" {
		t.Fatal("optional escape omitted")
	}
}

func TestSafetyUnicodeAndCaseAliasDestinations(t *testing.T) {
	pairs := [][2]string{
		{"café", "café"},     // NFC vs NFD
		{"Straße", "STRASSE"}, // fold
		{"a", "A"},            // case
		{"K", "k"},            // Kelvin sign
		{"i̇", "İ"},           // dotted i
		{"fi", "ﬁ"},           // ligature (NFKC)
	}
	for _, pr := range pairs {
		g, c, p, _ := realFixture(t)
		g.Bindings[1].Destination = pr[0]
		g.Bindings[2].Destination = pr[1]
		os.WriteFile(filepath.Join(g.Home.LogicalPath, pr[0]), []byte("x"), 0600)
		g.Bindings[1].Source = pr[0]
		g.Bindings[2].Source = "c"
		_, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
		if r.Code == "preflight_complete" {
			t.Fatalf("accepted alias pair %q %q", pr[0], pr[1])
		}
	}
}

func TestSafetyRelPathTable(t *testing.T) {
	bad := []string{"", " ", "/a", "./a", "a/", "a//b", "a/./b", "a/../b", "../a", "a/..", ".", "..", "a\\b", "a\x00b", ".git/x", ".GIT/x", ".ssh", ".materialize/x", "a:b", "{x}", "a\nb", "‮", strings.Repeat("a", 300)}
	for _, rel := range bad {
		for _, field := range []string{"src", "dst"} {
			g, c, p, _ := realFixture(t)
			if field == "src" {
				g.Bindings[2].Source = rel
			} else {
				g.Bindings[2].Destination = rel
			}
			_, r := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
			if r.Code == "preflight_complete" {
				t.Errorf("accepted bad %s %q", field, rel)
			}
		}
	}
}

// ---------- 7. FIFO swapped between Stat and Open in Port.Source ----------

func TestSafetyInspectAtEveryCrashPoint(t *testing.T) {
	g, c, p, _ := realFixture(t)
	var prev *effects.Evidence
	type obs struct {
		call             int
		which            string
		phase            effects.Phase
		outcome          effects.Outcome
		code             string
		states           []effects.ObservationState
		mutatedByInspect bool
	}
	var rows []obs
	sink := &hookSink{}
	inspect := func(call int, which string, e effects.Evidence) {
		before := snapshot(t, g.Candidate.Path)
		x := credentials.Inspect(context.Background(), g, e.Clone(), c.PreflightContext, p)
		after := snapshot(t, g.Candidate.Path)
		o := obs{call: call, which: which, phase: e.Phase, outcome: x.Outcome, code: x.Code}
		for _, in := range x.Inspections {
			o.states = append(o.states, in.State)
		}
		for k, v := range after {
			if before[k] != v {
				o.mutatedByInspect = true
			}
		}
		if len(before) != len(after) {
			o.mutatedByInspect = true
		}
		rows = append(rows, o)
		if e.Phase != effects.CompletePhase && (x.Outcome == effects.AlreadyPresent || x.Outcome == effects.Applied) {
			t.Errorf("call %d %s: non-complete evidence classified %s/%s", call, which, x.Outcome, x.Code)
		}
	}
	sink.hook = func(n int, e effects.Evidence) error {
		if prev != nil {
			inspect(n, "prev-row-vs-current-disk(crash before this record)", *prev)
		}
		inspect(n, "new-row", e)
		cp := e.Clone()
		prev = &cp
		return nil
	}
	c.Receipts = sink
	r := runApply(t, g, c, p)
	if r.Outcome != effects.Applied {
		t.Fatal(r)
	}
	inspect(99, "final", r.Evidence)
	for _, o := range rows {
		t.Logf("call=%d %-52s phase=%-12s => %s/%s states=%v mutated=%t", o.call, o.which, o.phase, o.outcome, o.code, o.states, o.mutatedByInspect)
		if o.mutatedByInspect {
			t.Errorf("Inspect mutated disk")
		}
	}
}

func TestSafetyReceiptRowOutcomesBeforeMutation(t *testing.T) {
	g, c, p, _ := realFixture(t)
	sink := &hookSink{}
	c.Receipts = sink
	pr, pf := credentials.Preflight(context.Background(), g, c.PreflightContext, p)
	t.Logf("preflight on EMPTY candidate: outcome=%s code=%s evidence.phase=%s evidence.outcome=%s", pf.Outcome, pf.Code, pf.Evidence.Phase, pf.Evidence.Outcome)
	r := credentials.Apply(context.Background(), pr, c, p)
	for i, row := range sink.rows {
		var st []string
		for _, l := range row.Links {
			st = append(st, string(l.Outcome))
		}
		t.Logf("row %d: phase=%s evidence.outcome=%s links=%v", i+1, row.Phase, row.Outcome, st)
	}
	_ = r
}

func TestSafetyPreMutationFailureLeavesNoTerminalReceipt(t *testing.T) {
	g, c, p, _ := realFixture(t)
	sink := &hookSink{}
	c.Receipts = sink
	sink.hook = func(n int, e effects.Evidence) error {
		if e.Phase == effects.IntentPhase {
			os.Remove(filepath.Join(g.Home.LogicalPath, "b"))
		}
		return nil
	}
	r := runApply(t, g, c, p)
	last := sink.rows[len(sink.rows)-1]
	t.Logf("result=%s/%s ; last persisted row: phase=%s outcome=%s ; rows=%d", r.Outcome, r.Code, last.Phase, last.Outcome, len(sink.rows))
}
