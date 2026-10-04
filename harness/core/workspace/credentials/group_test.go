package credentials

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

var fixtureErr = errors.New("fixture failure")

type fakePort struct {
	links                                map[string]LinkObservation
	sources                              map[string]bool
	creates, removes                     int
	failCreate, failRemove               int
	unsupported, failClose, failValidate bool
	onCreate                             func(*fakePort, string)
}

func (f *fakePort) Supported() bool { return !f.unsupported }
func (f *fakePort) Source(_ context.Context, h ResolvedHome, p string) (SourceObservation, error) {
	if !f.sources[p] {
		return SourceObservation{}, ErrSourceAbsent
	}
	return SourceObservation{LogicalPath: filepath.Join(h.LogicalPath, p), CanonicalPath: filepath.Join(h.CanonicalPath, p), Accessible: f.sources[p]}, nil
}
func (f *fakePort) Destination(_ context.Context, _ effects.RootInput, p string) (LinkObservation, error) {
	return f.observe(p), nil
}
func (f *fakePort) OpenCandidate(context.Context, effects.RootInput) (CandidateSession, error) {
	return f, nil
}
func (f *fakePort) Validate(context.Context) error {
	if f.failValidate {
		return fixtureErr
	}
	return nil
}
func (f *fakePort) observe(p string) LinkObservation {
	if o, ok := f.links[p]; ok {
		return o
	}
	return LinkObservation{ParentIdentity: "parent"}
}
func (f *fakePort) Inspect(_ context.Context, p string) (LinkObservation, error) {
	return f.observe(p), nil
}
func (f *fakePort) CreateExclusive(_ context.Context, source, p string) (LinkObservation, error) {
	f.creates++
	if f.onCreate != nil {
		f.onCreate(f, p)
	}
	if f.creates == f.failCreate || f.links[p].Exists {
		return LinkObservation{}, fixtureErr
	}
	o := LinkObservation{Exists: true, IsLink: true, Target: source, Identity: fmt.Sprintf("new-%d", f.creates), ParentIdentity: "parent"}
	f.links[p] = o
	return o, nil
}
func (f *fakePort) RemoveIfMatches(_ context.Context, p string, want LinkObservation) error {
	f.removes++
	if f.removes == f.failRemove || f.links[p] != want {
		return fixtureErr
	}
	delete(f.links, p)
	return nil
}
func (f *fakePort) Close() error {
	if f.failClose {
		return fixtureErr
	}
	return nil
}

type sink struct {
	calls, fail int
	evidence    []effects.Evidence
	hook        func(int)
}

func (s *sink) Record(_ context.Context, e effects.Evidence) error {
	s.calls++
	if s.hook != nil {
		s.hook(s.calls)
	}
	if s.calls == s.fail {
		return fixtureErr
	}
	s.evidence = append(s.evidence, e.Clone())
	return nil
}

func fixture() (Group, effects.ApplyContext, *fakePort, *sink) {
	i, o := homeInput()
	h, e := ResolveRealHome(i, o)
	if e != nil {
		panic(e)
	}
	g := Group{Header: effects.Header{Version: effects.SchemaVersion, OperationID: "operation", InputDigest: "digest"}, Layer: "boot", Home: h, Candidate: effects.RootInput{ID: "candidate", Path: "/candidate/inactive", AllowedBase: "/candidate", MutationIdentity: "/candidate", Owner: "fixture", Provenance: "host", Inactive: true, PrivateCustody: true}}
	f := &fakePort{links: map[string]LinkObservation{}, sources: map[string]bool{}}
	for _, name := range []string{"a", "b", "c"} {
		g.Bindings = append(g.Bindings, Binding{Source: name, Destination: name, Required: true, AuthorizationID: "grant", AuthorizationVersion: "version", SourceRead: true})
		f.sources[name] = true
	}
	s := &sink{}
	c := effects.ApplyContext{PreflightContext: effects.PreflightContext{Header: g.Header, HeldLocks: []effects.LockIdentity{{Namespace: "/locks", CanonicalID: "/candidate"}}, Validate: func(context.Context) error { return nil }}, ArtifactGeneration: "generation", ArtifactRootID: "candidate", Receipts: s}
	return g, c, f, s
}
func apply(t *testing.T, g Group, c effects.ApplyContext, f *fakePort) effects.Result {
	t.Helper()
	p, r := Preflight(context.Background(), g, c.PreflightContext, f)
	if r.Code != "preflight_complete" {
		t.Fatalf("preflight: %+v", r)
	}
	return Apply(context.Background(), p, c, f)
}

func TestGroupPreflightGuards(t *testing.T) {
	guards := map[string]func(*Group, *effects.ApplyContext, *fakePort){
		"version":          func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Header.Version = "future" },
		"operation":        func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Header.OperationID = "other" },
		"digest":           func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Header.InputDigest = "other" },
		"installed":        func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Layer = "installed" },
		"active":           func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Candidate.Inactive = false },
		"custody":          func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Candidate.PrivateCustody = false },
		"owner":            func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Candidate.Owner = "" },
		"base-escape":      func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Candidate.Path = "/outside" },
		"namespace-inside": func(g *Group, c *effects.ApplyContext, f *fakePort) { c.HeldLocks[0].Namespace = "/candidate/locks" },
		"lock":             func(g *Group, c *effects.ApplyContext, f *fakePort) { c.HeldLocks = nil },
		"authority":        func(g *Group, c *effects.ApplyContext, f *fakePort) { c.Validate = nil },
		"denied": func(g *Group, c *effects.ApplyContext, f *fakePort) {
			c.Validate = func(context.Context) error { return fixtureErr }
		},
		"grant":                func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Bindings[2].AuthorizationID = "" },
		"read":                 func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Bindings[2].SourceRead = false },
		"write-separate":       func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Bindings[2].SourceWrite = true },
		"source-path":          func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Bindings[2].Source = "../outside" },
		"destination-path":     func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Bindings[2].Destination = "../outside" },
		"alias":                func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Bindings[2].Destination = "A" },
		"ancestor-alias":       func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Bindings[2].Destination = "a/nested" },
		"captured-before":      func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Home.BeforeRedirect = false },
		"source-in-candidate":  func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Home.CanonicalPath = "/candidate/home" },
		"unsupported":          func(g *Group, c *effects.ApplyContext, f *fakePort) { f.unsupported = true },
		"required-unavailable": func(g *Group, c *effects.ApplyContext, f *fakePort) { f.sources["c"] = false },
		"unproven-existing": func(g *Group, c *effects.ApplyContext, f *fakePort) {
			f.links["c"] = LinkObservation{Exists: true, IsLink: true, Target: "/resource/provider/c"}
		},
		"late-conflict": func(g *Group, c *effects.ApplyContext, f *fakePort) { f.links["c"] = LinkObservation{Exists: true} },
		"wrong-link": func(g *Group, c *effects.ApplyContext, f *fakePort) {
			f.links["c"] = LinkObservation{Exists: true, IsLink: true, Target: "/other"}
		},
	}
	for name, mutate := range guards {
		t.Run(name, func(t *testing.T) {
			g, c, f, _ := fixture()
			mutate(&g, &c, f)
			before := map[string]LinkObservation{}
			for k, v := range f.links {
				before[k] = v
			}
			_, r := Preflight(context.Background(), g, c.PreflightContext, f)
			if r.Code == "preflight_complete" || f.creates != 0 || f.removes != 0 || !reflect.DeepEqual(before, f.links) {
				t.Fatalf("guard missed: %+v", r)
			}
		})
	}
}

func TestGroupFailureAtEveryPosition(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for index := 1; index <= 3; index++ {
			t.Run(fmt.Sprintf("existing=%t/failure=%d", existing, index), func(t *testing.T) {
				g, c, f, _ := fixture()
				if existing {
					f.links["a"] = LinkObservation{Exists: true, IsLink: true, Target: "/resource/provider/a", Identity: "old", ParentIdentity: "parent"}
				}
				f.failCreate = index
				r := apply(t, g, c, f)
				if index <= 3-boolInt(existing) {
					if index == 1 && r.Outcome != effects.Conflict {
						t.Fatalf("before-mutation: %+v", r)
					}
					if index > 1 && r.Outcome != effects.Partial {
						t.Fatalf("after-mutation: %+v", r)
					}
					want := 0
					if existing {
						want = 1
					}
					if len(f.links) != want {
						t.Fatalf("links not compensated: %v", f.links)
					}
				} else if r.Outcome != effects.Applied {
					t.Fatal(r)
				}
				if existing && f.links["a"].Identity != "old" {
					t.Fatal("preexisting link touched")
				}
			})
		}
	}
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestGroupReceiptFailureAtEveryPosition(t *testing.T) {
	for index := 1; index <= 8; index++ {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			g, c, f, s := fixture()
			s.fail = index
			r := apply(t, g, c, f)
			if index <= 2 {
				if f.creates != 0 || len(f.links) != 0 {
					t.Fatal("mutation before intent")
				}
			} else if r.Outcome != effects.Partial || len(f.links) != 0 {
				t.Fatalf("failure accounting: %+v, %v", r, f.links)
			}
		})
	}
}

func TestGroupRollbackRetainsChangedAndUncertain(t *testing.T) {
	for _, kind := range []string{"swapped", "remove-failure", "custody", "fence"} {
		t.Run(kind, func(t *testing.T) {
			g, c, f, _ := fixture()
			f.failCreate = 2
			switch kind {
			case "swapped":
				f.onCreate = func(p *fakePort, _ string) {
					if p.creates == 2 {
						v := p.links["a"]
						v.Identity = "other"
						p.links["a"] = v
					}
				}
			case "remove-failure":
				f.failRemove = 1
			case "custody":
				f.onCreate = func(p *fakePort, _ string) {
					if p.creates == 2 {
						p.failValidate = true
					}
				}
			case "fence":
				c.Validate = func(context.Context) error {
					if f.creates >= 2 {
						return fixtureErr
					}
					return nil
				}
			}
			r := apply(t, g, c, f)
			if r.Outcome != effects.Partial || !f.links["a"].Exists || len(r.Obligations) == 0 {
				t.Fatalf("unsafe compensation: %+v %v", r, f.links)
			}
		})
	}
}

func TestGroupIdempotenceReplayAndInputIsolation(t *testing.T) {
	g, c, f, s := fixture()
	prepared, pre := Preflight(context.Background(), g, c.PreflightContext, f)
	if pre.Code != "preflight_complete" {
		t.Fatal(pre)
	}
	g.Bindings[0].Destination = "changed"
	g.Home.PlantedRoots[0] = "/changed"
	r := Apply(context.Background(), prepared, c, f)
	if r.Outcome != effects.Applied || f.creates != 3 || f.links["changed"].Exists {
		t.Fatal(r)
	}
	g, c, _, _ = fixture()
	r2 := apply(t, g, c, f)
	if r2.Outcome != effects.AlreadyPresent || f.creates != 3 || f.removes != 0 {
		t.Fatal(r2)
	}
	r3 := Inspect(context.Background(), g, r.Evidence, c.PreflightContext, f)
	if r3.Code != "inspected_complete" || f.creates != 3 {
		t.Fatal(r3)
	}
	for _, field := range []string{"header", "auth", "source", "identity", "required-omitted", "phase", "group-outcome", "created-kind"} {
		t.Run(field, func(t *testing.T) {
			e := r.Evidence.Clone()
			switch field {
			case "header":
				e.Header.InputDigest = "other"
			case "auth":
				e.Links[0].AuthorizationID = "other"
			case "source":
				e.Links[0].Target = "other"
			case "identity":
				e.Links[0].LinkIdentity = "other"
			case "required-omitted":
				saved := f.links["a"]
				delete(f.links, "a")
				defer func() { f.links["a"] = saved }()
				e.Links[0].Outcome = effects.Omitted
				e.Links[0].Created = false
				e.Links[0].LinkIdentity = ""
				e.Links[0].Source = ""
				e.Links[0].Target = ""
				e.Links[0].ParentIdentity = ""
			case "phase":
				e.Phase = "intent"
			case "group-outcome":
				e.Outcome = effects.Partial
			case "created-kind":
				e.Links[0].Created = false
			}
			x := Inspect(context.Background(), g, e, c.PreflightContext, f)
			if x.Code == "inspected_complete" {
				t.Fatalf("bad evidence accepted: %s", field)
			}
		})
	}
	r.Evidence.Links[0].Target = "changed"
	if s.evidence[len(s.evidence)-1].Links[0].Target == "changed" {
		t.Fatal("receipt aliased")
	}
}

func TestGroupOptionalOmissionAndApplyEvidence(t *testing.T) {
	g, c, f, _ := fixture()
	g.Bindings[2].Required = false
	f.sources["c"] = false
	r := apply(t, g, c, f)
	if r.Outcome != effects.Applied || f.creates != 2 || r.Evidence.Links[2].Outcome != effects.Omitted {
		t.Fatal(r)
	}
	for _, kind := range []string{"sink", "generation", "root", "prepared", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			g, c, f, _ := fixture()
			p, _ := Preflight(context.Background(), g, c.PreflightContext, f)
			ctx := context.Background()
			switch kind {
			case "sink":
				c.Receipts = nil
			case "generation":
				c.ArtifactGeneration = ""
			case "root":
				c.ArtifactRootID = "other"
			case "prepared":
				p = PreparedGroup{}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			r := Apply(ctx, p, c, f)
			if r.Code == "group_complete" || f.creates != 0 {
				t.Fatal(r)
			}
		})
	}
}

func TestFinalSourceChangeAndCloseFailureRemainPartial(t *testing.T) {
	for _, kind := range []string{"source", "close"} {
		t.Run(kind, func(t *testing.T) {
			g, c, f, s := fixture()
			if kind == "source" {
				s.hook = func(_ int) {
					if f.creates == 3 {
						f.sources["a"] = false
					}
				}
			} else {
				f.failClose = true
			}
			r := apply(t, g, c, f)
			if r.Outcome != effects.Partial || len(r.Obligations) == 0 {
				t.Fatal(r)
			}
			if kind == "source" && len(f.links) != 0 {
				t.Fatal("created links not compensated")
			}
			if kind == "close" && s.evidence[len(s.evidence)-1].Phase != "interrupted" {
				t.Fatal("close failure absent from receipt")
			}
		})
	}
}
