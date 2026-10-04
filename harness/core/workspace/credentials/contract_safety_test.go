package credentials

import (
	"context"
	"fmt"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

func hasCode(r effects.Result, code string) bool {
	for _, o := range r.Obligations {
		if o.Code == code {
			return true
		}
	}
	return false
}

// ---- wrappers -------------------------------------------------------------

type viewPort struct {
	*fakePort
	source      func(ResolvedHome, string) (SourceObservation, error)
	inspect     func(string) (LinkObservation, bool)
	create      func(LinkObservation, error) (LinkObservation, error)
	remove      func(context.Context) error
	nilSession  bool
	inspectCall int
}

func (p *viewPort) Source(ctx context.Context, h ResolvedHome, rel string) (SourceObservation, error) {
	if p.source != nil {
		return p.source(h, rel)
	}
	return p.fakePort.Source(ctx, h, rel)
}
func (p *viewPort) OpenCandidate(context.Context, effects.RootInput) (CandidateSession, error) {
	if p.nilSession {
		return nil, nil
	}
	return &viewSession{p.fakePort, p}, nil
}

type viewSession struct {
	*fakePort
	port *viewPort
}

func (s *viewSession) Inspect(ctx context.Context, rel string) (LinkObservation, error) {
	s.port.inspectCall++
	if s.port.inspect != nil {
		if o, ok := s.port.inspect(rel); ok {
			return o, nil
		}
	}
	return s.fakePort.Inspect(ctx, rel)
}
func (s *viewSession) CreateExclusive(ctx context.Context, src, rel string) (LinkObservation, error) {
	o, e := s.fakePort.CreateExclusive(ctx, src, rel)
	if s.port.create != nil {
		return s.port.create(o, e)
	}
	return o, e
}
func (s *viewSession) RemoveIfMatches(ctx context.Context, rel string, w LinkObservation) error {
	if s.port.remove != nil {
		if e := s.port.remove(ctx); e != nil {
			return e
		}
	}
	return s.fakePort.RemoveIfMatches(ctx, rel, w)
}

func preflightCode(g Group, c effects.ApplyContext, p LinkPort) string {
	_, r := Preflight(context.Background(), g, c.PreflightContext, p)
	return r.Code
}

// ---- binding / home -------------------------------------------------------

func TestContractBindingGuards(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Group, *effects.ApplyContext, *fakePort)
		want   string
	}{
		"lock-for-other-identity": {func(g *Group, c *effects.ApplyContext, f *fakePort) { c.HeldLocks[0].CanonicalID = "/other" }, "missing_lock"},
		"lock-empty-namespace":    {func(g *Group, c *effects.ApplyContext, f *fakePort) { c.HeldLocks[0].Namespace = "" }, "missing_lock"},
		"lock-relative-namespace": {func(g *Group, c *effects.ApplyContext, f *fakePort) { c.HeldLocks[0].Namespace = "locks" }, "missing_lock"},
		"both-headers-future": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Header.Version = "future"
			c.Header = g.Header
		}, "invalid_binding"},
		"both-headers-no-operation": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Header.OperationID = ""
			c.Header = g.Header
		}, "invalid_binding"},
		"both-headers-no-digest": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Header.InputDigest = ""
			c.Header = g.Header
		}, "invalid_binding"},
		"candidate-no-id":         {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Candidate.ID = ""; c.ArtifactRootID = "" }, "candidate_refused"},
		"candidate-no-provenance": {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Candidate.Provenance = "" }, "candidate_refused"},
		"candidate-relative":      {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Candidate.Path = "candidate/inactive" }, "candidate_refused"},
		"base-relative":           {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Candidate.AllowedBase = "candidate" }, "candidate_refused"},
		"path-equals-base":        {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Candidate.Path = "/candidate" }, "candidate_refused"},
		"path-equals-identity": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Candidate.AllowedBase = "/"
			g.Candidate.Path = "/candidate"
		}, "candidate_refused"},
		"identity-relative": {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Candidate.MutationIdentity = "candidate" }, "candidate_refused"},
		"path-outside-identity-inside-base": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Candidate.AllowedBase = "/"
			g.Candidate.Path = "/elsewhere/inactive"
		}, "candidate_refused"},
		"path-outside-base-inside-identity": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Candidate.AllowedBase = "/candidate/sub"
		}, "candidate_refused"},
		"home-no-provider":      {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Home.Provider = "" }, "home_refused"},
		"home-no-capture":       {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Home.CaptureID = "" }, "home_refused"},
		"home-no-revision":      {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Home.Revision = "" }, "home_refused"},
		"home-no-provenance":    {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Home.Provenance = "" }, "home_refused"},
		"home-logical-relative": {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Home.LogicalPath = "provider" }, "home_refused"},
		"home-canonical-relative": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Home.CanonicalPath = "provider"
		}, "home_refused"},
		"home-base-relative": {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Home.CanonicalBase = "resource" }, "home_refused"},
		"home-outside-base":  {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Home.CanonicalBase = "/other" }, "home_refused"},
		"home-canonical-in-candidate": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Home.CanonicalBase = "/"
			g.Home.CanonicalPath = "/candidate/home"
			g.Home.PlantedRoots = []string{"/redirected"}
		}, "home_refused"},
		"home-logical-in-candidate": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Home.LogicalPath = "/candidate/home"
			g.Home.PlantedRoots = []string{"/redirected"}
		}, "home_refused"},
		"planted-contains-canonical-only": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Home.CanonicalPath = "/resource/real/x"
			g.Home.PlantedRoots = []string{"/resource/real"}
		}, "home_refused"},
		"path-equals-base-not-identity": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Candidate.AllowedBase = "/candidate/inactive"
		}, "candidate_refused"},
		"home-no-planted": {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Home.PlantedRoots = nil }, "home_refused"},
		"planted-contains-canonical-home": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Home.PlantedRoots = []string{"/resource"}
		}, "home_refused"},
		"planted-contains-logical-home": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Home.CanonicalPath = "/resource/real"
			g.Home.PlantedRoots = []string{"/resource/provider"}
		}, "home_refused"},
		"planted-relative": {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Home.PlantedRoots = []string{"candidate"} }, "home_refused"},
		"no-bindings":      {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Bindings = nil }, "invalid_group"},
		"too-many-bindings": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Bindings = nil
			for i := 0; i <= MaxBindings; i++ {
				n := fmt.Sprintf("f%05d", i)
				g.Bindings = append(g.Bindings, Binding{Source: n, Destination: n, Required: true, AuthorizationID: "g", AuthorizationVersion: "v", SourceRead: true})
				f.sources[n] = true
			}
		}, "invalid_group"},
		"auth-version": {func(g *Group, c *effects.ApplyContext, f *fakePort) { g.Bindings[1].AuthorizationVersion = "" }, "binding_refused"},
		"source-traversal": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Bindings[1].Source = "../x"
			f.sources["../x"] = true
		}, "binding_refused"},
		"write-auth-id-only": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Bindings[1].SourceWrite = true
			g.Bindings[1].SourceWriteAuthorizationVersion = "v"
		}, "binding_refused"},
		"write-auth-ver-only": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Bindings[1].SourceWrite = true
			g.Bindings[1].SourceWriteAuthorizationID = "id"
		}, "binding_refused"},
		"alias-deeper-sorts-first": {func(g *Group, c *effects.ApplyContext, f *fakePort) {
			g.Bindings = append(g.Bindings, Binding{Source: "a", Destination: "A/nested", Required: true, AuthorizationID: "g", AuthorizationVersion: "v", SourceRead: true})
		}, "destination_alias"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g, c, f, _ := fixture()
			tc.mutate(&g, &c, f)
			if got := preflightCode(g, c, f); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if f.creates != 0 {
				t.Fatal("mutation")
			}
		})
	}
}

func TestContractResolveRealHome(t *testing.T) {
	cases := map[string]func(*CapturedProviderHome, *HomeObservations){
		"no-provider":         func(i *CapturedProviderHome, o *HomeObservations) { i.Provider = "" },
		"path-relative-only":  func(i *CapturedProviderHome, o *HomeObservations) { i.Path = "resource/provider" },
		"base-relative":       func(i *CapturedProviderHome, o *HomeObservations) { i.AllowedBase = "resource" },
		"both-relative":       func(i *CapturedProviderHome, o *HomeObservations) { i.Path = "provider"; i.AllowedBase = "." },
		"canon-home-relative": func(i *CapturedProviderHome, o *HomeObservations) { o.CanonicalHome = "provider" },
		"canon-base-relative": func(i *CapturedProviderHome, o *HomeObservations) { o.CanonicalBase = "resource" },
		"logical-outside-base": func(i *CapturedProviderHome, o *HomeObservations) {
			i.Path = "/elsewhere/provider"
		},
		"no-planted": func(i *CapturedProviderHome, o *HomeObservations) {
			i.PlantedRoots = nil
			o.CanonicalPlantedRoots = nil
		},
		"planted-relative-logical":   func(i *CapturedProviderHome, o *HomeObservations) { i.PlantedRoots = []string{"candidate"} },
		"planted-relative-canonical": func(i *CapturedProviderHome, o *HomeObservations) { o.CanonicalPlantedRoots = []string{"candidate"} },
		"planted-contains-logical":   func(i *CapturedProviderHome, o *HomeObservations) { i.PlantedRoots = []string{"/resource"} },
		"planted-contains-canonical": func(i *CapturedProviderHome, o *HomeObservations) { o.CanonicalPlantedRoots = []string{"/resource"} },
		"path-nul":                   func(i *CapturedProviderHome, o *HomeObservations) { i.Path = "/resource/pro\x00vider" },
		"path-unclean":               func(i *CapturedProviderHome, o *HomeObservations) { i.Path = "/resource/x/../provider" },
		"canon-unclean":              func(i *CapturedProviderHome, o *HomeObservations) { o.CanonicalHome = "/resource/x/../provider" },
		"path-dotdot-escape": func(i *CapturedProviderHome, o *HomeObservations) {
			i.AllowedBase = "/resource/provider/sub"
			i.Path = "/resource/provider"
		},
		"canon-sibling-dotdot": func(i *CapturedProviderHome, o *HomeObservations) {
			o.CanonicalBase = "/resource/provider/x"
			o.CanonicalHome = "/resource/provider"
		},
		"planted-unclean": func(i *CapturedProviderHome, o *HomeObservations) { i.PlantedRoots = []string{"/candidate/../x"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			i, o := homeInput()
			mutate(&i, &o)
			if _, err := ResolveRealHome(i, o); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// ---- apply control flow ---------------------------------------------------

func TestContractStopsMutatingAfterAuthorityLoss(t *testing.T) {
	for _, kind := range []string{"fence", "cancel", "custody", "source"} {
		t.Run(kind, func(t *testing.T) {
			g, c, f, s := fixture()
			ctx := context.Background()
			switch kind {
			case "fence":
				c.Validate = func(context.Context) error {
					if f.creates >= 2 {
						return fixtureErr
					}
					return nil
				}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				s.hook = func(n int) {
					if f.creates == 2 && n == 5 {
						cancel()
					}
				}
			case "custody":
				f.onCreate = func(p *fakePort, _ string) {
					if p.creates == 2 {
						p.failValidate = true
					}
				}
			case "source":
				s.hook = func(n int) {
					if f.creates == 2 && n == 5 {
						f.sources["c"] = false
					}
				}
			}
			p, pre := Preflight(context.Background(), g, c.PreflightContext, f)
			if pre.Code != "preflight_complete" {
				t.Fatal(pre)
			}
			r := Apply(ctx, p, c, f)
			wantLinks := 0
			if kind == "fence" || kind == "custody" {
				wantLinks = 2 // authority lost: compensation must retain, not remove
			}
			if r.Outcome != effects.Partial || f.creates != 2 || len(f.links) != wantLinks || !hasCode(r, "recovery_required") {
				t.Fatalf("outcome=%s code=%s creates=%d links=%v obl=%v", r.Outcome, r.Code, f.creates, f.links, r.Obligations)
			}
			if wantLinks == 2 && !hasCode(r, "link_retained") {
				t.Fatal("retained links not reported")
			}
		})
	}
}

func TestContractFenceLostAfterLastCreateBlocksVerification(t *testing.T) {
	g, c, f, s := fixture()
	lost := false
	c.Validate = func(context.Context) error {
		if lost {
			return fixtureErr
		}
		return nil
	}
	s.hook = func(n int) {
		if n == 7 { // third link_created record
			lost = true
		}
	}
	r := apply(t, g, c, f)
	if r.Outcome != effects.Partial || r.Code != "authority_or_candidate_changed" || len(f.links) != 3 || !hasCode(r, "link_retained") {
		t.Fatalf("%s %s %v", r.Outcome, r.Code, f.links)
	}
}

func TestContractRetainedBookkeepingAndOrder(t *testing.T) {
	g, c, f, s := fixture()
	f.failCreate = 3
	f.failRemove = 1 // first removal attempt: reverse order means link b
	r := apply(t, g, c, f)
	if r.Outcome != effects.Partial || !f.links["b"].Exists || f.links["a"].Exists {
		t.Fatalf("order/retention: %v", f.links)
	}
	if r.Evidence.Links[1].Outcome != effects.Partial || r.Evidence.Links[0].Outcome != effects.Removed {
		t.Fatalf("bookkeeping lies: %+v", r.Evidence.Links)
	}
	n := 0
	for _, o := range r.Obligations {
		if o.Code == "link_retained" {
			n++
		}
	}
	if n != 1 || !hasCode(r, "recovery_required") {
		t.Fatalf("obligations %v", r.Obligations)
	}
	last := s.evidence[len(s.evidence)-1]
	if last.Phase != effects.InterruptedPhase || last.Links[1].Outcome != effects.Partial {
		t.Fatalf("receipt: %+v", last)
	}
}

func TestContractCleanupSurvivesCancellation(t *testing.T) {
	g, c, f, s := fixture()
	ctx, cancel := context.WithCancel(context.Background())
	s.hook = func(n int) {
		if n == 5 {
			cancel()
		}
	}
	pp := &viewPort{fakePort: f, remove: func(ctx context.Context) error { return ctx.Err() }}
	p, _ := Preflight(context.Background(), g, c.PreflightContext, pp)
	r := Apply(ctx, p, c, pp)
	if r.Outcome != effects.Partial || len(f.links) != 0 {
		t.Fatalf("cleanup aborted by cancellation: %s %s %v", r.Outcome, r.Code, f.links)
	}
}

func TestContractReceiptPendingObligation(t *testing.T) {
	g, c, f, s := fixture()
	f.failCreate = 3
	s.fail = 7 // the post-compensation record
	r := apply(t, g, c, f)
	if r.Outcome != effects.Partial || !hasCode(r, "receipt_pending") || !hasCode(r, "recovery_required") {
		t.Fatalf("%s %v", r.Outcome, r.Obligations)
	}
	g, c, f, s = fixture()
	f.failClose = true
	s.fail = 9 // record written by the close failure path
	r = apply(t, g, c, f)
	if r.Outcome != effects.Partial || !hasCode(r, "receipt_pending") || !hasCode(r, "recovery_required") {
		t.Fatalf("close path %s %v", r.Outcome, r.Obligations)
	}
}

func TestContractNilSession(t *testing.T) {
	g, c, f, _ := fixture()
	pp := &viewPort{fakePort: f, nilSession: true}
	p, _ := Preflight(context.Background(), g, c.PreflightContext, pp)
	r := Apply(context.Background(), p, c, pp)
	if r.Outcome != effects.Unsupported || r.Code != "candidate_unsupported" {
		t.Fatal(r)
	}
}

func TestContractSessionViewConflictBeforeFirstLink(t *testing.T) {
	cases := map[string]LinkObservation{
		"file":          {Exists: true, ParentIdentity: "parent", Identity: "x"},
		"wrong-target":  {Exists: true, IsLink: true, Target: "/other", Identity: "x", ParentIdentity: "parent"},
		"no-identity":   {Exists: true, IsLink: true, Target: "/resource/provider/b", ParentIdentity: "parent"},
		"no-parent":     {Exists: true, IsLink: true, Target: "/resource/provider/b", Identity: "x"},
		"exact-but-new": {Exists: true, IsLink: true, Target: "/resource/provider/b", Identity: "x", ParentIdentity: "parent"}, // allowed: AlreadyPresent
	}
	for name, view := range cases {
		t.Run(name, func(t *testing.T) {
			g, c, f, _ := fixture()
			pp := &viewPort{fakePort: f, inspect: func(rel string) (LinkObservation, bool) {
				if rel == "b" {
					return view, true
				}
				return LinkObservation{}, false
			}}
			p, _ := Preflight(context.Background(), g, c.PreflightContext, pp)
			r := Apply(context.Background(), p, c, pp)
			if name == "exact-but-new" {
				return
			}
			if r.Outcome != effects.Conflict || r.Code != "destination_conflict" || f.creates != 0 {
				t.Fatalf("%s %s creates=%d", r.Outcome, r.Code, f.creates)
			}
		})
	}
}

func TestContractPortObservationsAreValidated(t *testing.T) {
	cases := map[string]func(LinkObservation, error) (LinkObservation, error){
		"target":       func(o LinkObservation, e error) (LinkObservation, error) { o.Target = "/elsewhere"; return o, e },
		"identity":     func(o LinkObservation, e error) (LinkObservation, error) { o.Identity = ""; return o, e },
		"parent":       func(o LinkObservation, e error) (LinkObservation, error) { o.ParentIdentity = ""; return o, e },
		"not-link":     func(o LinkObservation, e error) (LinkObservation, error) { o.IsLink = false; return o, e },
		"exists-false": func(o LinkObservation, e error) (LinkObservation, error) { o.Exists = false; return o, nil },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			g, c, f, _ := fixture()
			pp := &viewPort{fakePort: f, create: fn}
			p, _ := Preflight(context.Background(), g, c.PreflightContext, pp)
			r := Apply(context.Background(), p, c, pp)
			if r.Outcome == effects.Applied || r.Code == "group_complete" {
				t.Fatalf("accepted bad created observation: %s %s", r.Outcome, r.Code)
			}
		})
	}
}

func TestContractVerificationDetectsSwapAfterCreate(t *testing.T) {
	for _, kind := range []string{"identity", "target", "parent", "removed", "not-link"} {
		t.Run(kind, func(t *testing.T) {
			g, c, f, s := fixture()
			s.hook = func(n int) {
				if n == 7 { // after the last link_created record
					v := f.links["c"]
					switch kind {
					case "identity":
						v.Identity = "swapped"
					case "target":
						v.Target = "/elsewhere"
					case "parent":
						v.ParentIdentity = "other-parent"
					case "removed":
						delete(f.links, "c")
						return
					case "not-link":
						v.IsLink = false
					}
					f.links["c"] = v
				}
			}
			r := apply(t, g, c, f)
			if r.Outcome != effects.Partial || r.Code != "verification_failed" || !hasCode(r, "recovery_required") {
				t.Fatalf("%s %s %v", r.Outcome, r.Code, r.Obligations)
			}
			if kind != "removed" && !f.links["c"].Exists {
				t.Fatal("swapped link removed")
			}
		})
	}
}

func TestContractHandlerRevalidatesPortSources(t *testing.T) {
	good := func(h ResolvedHome, rel string) SourceObservation {
		return SourceObservation{LogicalPath: h.LogicalPath + "/" + rel, CanonicalPath: h.CanonicalPath + "/" + rel, Accessible: true}
	}
	cases := map[string]func(*Group, *SourceObservation){
		"canonical-relative": func(g *Group, o *SourceObservation) { o.CanonicalPath = "relative" },
		"canonical-outside-home": func(g *Group, o *SourceObservation) {
			o.CanonicalPath = "/elsewhere/x"
		},
		"canonical-in-candidate": func(g *Group, o *SourceObservation) {
			g.Candidate.MutationIdentity = "/resource/provider/boot"
			g.Candidate.AllowedBase = "/resource/provider/boot"
			g.Candidate.Path = "/resource/provider/boot/inactive"
			o.CanonicalPath = "/resource/provider/boot/inactive/x"
		},
		"canonical-in-planted": func(g *Group, o *SourceObservation) {
			g.Home.PlantedRoots = []string{"/resource/provider/redirected"}
			o.CanonicalPath = "/resource/provider/redirected/x"
		},
		"logical-differs": func(g *Group, o *SourceObservation) { o.LogicalPath = "/resource/provider/other" },
		"self-link": func(g *Group, o *SourceObservation) {
			g.Candidate.MutationIdentity = "/resource/provider/boot"
			g.Candidate.AllowedBase = "/resource/provider/boot"
			g.Candidate.Path = "/resource/provider/boot/inactive"
			g.Bindings = []Binding{{Source: "boot/inactive/x", Destination: "x", Required: true, AuthorizationID: "g", AuthorizationVersion: "v", SourceRead: true}}
			o.LogicalPath = "/resource/provider/boot/inactive/x"
			o.CanonicalPath = "/resource/provider/other/x"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			g, c, f, _ := fixture()
			var o SourceObservation
			pp := &viewPort{fakePort: f}
			mutate(&g, &o)
			pp.source = func(h ResolvedHome, rel string) (SourceObservation, error) {
				s := good(h, rel)
				if o.CanonicalPath != "" {
					s.CanonicalPath = o.CanonicalPath
				}
				if o.LogicalPath != "" {
					s.LogicalPath = o.LogicalPath
				}
				return s, nil
			}
			if len(g.Bindings) == 1 {
				c.HeldLocks = []effects.LockIdentity{{Namespace: "/locks", CanonicalID: g.Candidate.MutationIdentity}}
			} else if g.Candidate.MutationIdentity != "/candidate" {
				c.HeldLocks = []effects.LockIdentity{{Namespace: "/locks", CanonicalID: g.Candidate.MutationIdentity}}
			}
			got := preflightCode(g, c, pp)
			if got == "preflight_complete" || f.creates != 0 {
				t.Fatalf("accepted: %s", got)
			}
			t.Logf("code=%s", got)
		})
	}
}

func TestContractPreflightContextCancelled(t *testing.T) {
	g, c, f, _ := fixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, r := Preflight(ctx, g, c.PreflightContext, f)
	if r.Code != "authority_refused" {
		t.Fatal(r.Code)
	}
}

func TestContractPreflightDestinationShapes(t *testing.T) {
	cases := map[string]LinkObservation{
		"not-link":  {Exists: true, Identity: "x", ParentIdentity: "parent"},
		"no-id":     {Exists: true, IsLink: true, Target: "/resource/provider/c", ParentIdentity: "parent"},
		"no-parent": {Exists: true, IsLink: true, Target: "/resource/provider/c", Identity: "x"},
	}
	for name, view := range cases {
		t.Run(name, func(t *testing.T) {
			g, c, f, _ := fixture()
			f.links["c"] = view
			if got := preflightCode(g, c, f); got != "destination_conflict" {
				t.Fatal(got)
			}
		})
	}
}

// ---- Inspect --------------------------------------------------------------

func completeEvidence(t *testing.T) (Group, effects.ApplyContext, *fakePort, effects.Evidence) {
	g, c, f, _ := fixture()
	r := apply(t, g, c, f)
	if r.Outcome != effects.Applied {
		t.Fatal(r)
	}
	return g, c, f, r.Evidence.Clone()
}

func TestContractInspectRefusesMalformedEvidence(t *testing.T) {
	cases := map[string]func(*effects.Evidence){
		"kind":             func(e *effects.Evidence) { e.Kind = "other" },
		"root":             func(e *effects.Evidence) { e.RootID = "other" },
		"short":            func(e *effects.Evidence) { e.Links = e.Links[:2] },
		"long":             func(e *effects.Evidence) { e.Links = append(e.Links, e.Links[0]) },
		"phase":            func(e *effects.Evidence) { e.Phase = "bogus" },
		"phase-pre":        func(e *effects.Evidence) { e.Phase = effects.PreflightPhase; e.Outcome = effects.Partial },
		"outcome-int":      func(e *effects.Evidence) { e.Phase = effects.InterruptedPhase; e.Outcome = effects.Refused },
		"dest":             func(e *effects.Evidence) { e.Links[1].Destination = "other" },
		"auth-ver":         func(e *effects.Evidence) { e.Links[1].AuthorizationVersion = "other" },
		"source-only":      func(e *effects.Evidence) { e.Links[1].Source = "/resource/provider/other" },
		"target-only":      func(e *effects.Evidence) { e.Links[1].Target = "/resource/provider/other" },
		"applied-identity": func(e *effects.Evidence) { e.Links[1].LinkIdentity = "" },
		"applied-parent":   func(e *effects.Evidence) { e.Links[1].ParentIdentity = "" },
		"complete-pending": func(e *effects.Evidence) {
			e.Links[1].Outcome = effects.Pending
			e.Links[1].Created = false
			e.Links[1].LinkIdentity = ""
		},
		"complete-omitted-created": func(e *effects.Evidence) { e.Links[1].Outcome = effects.Omitted },
		"complete-omitted-identity": func(e *effects.Evidence) {
			e.Links[1].Outcome = effects.Omitted
			e.Links[1].Created = false
		},
		"already-present-created": func(e *effects.Evidence) { e.Links[1].Outcome = effects.AlreadyPresent },
		"already-present-identity": func(e *effects.Evidence) {
			e.Links[1].Outcome = effects.AlreadyPresent
			e.Links[1].Created = false
			e.Links[1].LinkIdentity = ""
		},
		"already-present-parent": func(e *effects.Evidence) {
			e.Links[1].Outcome = effects.AlreadyPresent
			e.Links[1].Created = false
			e.Links[1].ParentIdentity = ""
		},
		"complete-partial-entry": func(e *effects.Evidence) { e.Links[1].Outcome = effects.Partial },
		"interrupted-partial-not-created": func(e *effects.Evidence) {
			e.Phase = effects.InterruptedPhase
			e.Outcome = effects.Partial
			e.Links[1].Outcome = effects.Partial
			e.Links[1].Created = false
		},
		"complete-already-present-group-with-created": func(e *effects.Evidence) { e.Outcome = effects.AlreadyPresent },
		"interrupted-pending-created": func(e *effects.Evidence) {
			e.Phase = effects.IntentPhase
			e.Outcome = effects.Partial
			e.Links[1].Outcome = effects.Pending
		},
		"interrupted-pending-identity": func(e *effects.Evidence) {
			e.Phase = effects.IntentPhase
			e.Outcome = effects.Partial
			e.Links[1].Outcome = effects.Pending
			e.Links[1].Created = false
		},
		"interrupted-omitted-required": func(e *effects.Evidence) {
			e.Phase = effects.InterruptedPhase
			e.Outcome = effects.Partial
			e.Links[1].Outcome = effects.Omitted
			e.Links[1].Created = false
			e.Links[1].LinkIdentity = ""
			e.Links[1].Source = ""
			e.Links[1].Target = ""
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			g, c, f, e := completeEvidence(t)
			mutate(&e)
			x := Inspect(context.Background(), g, e, c.PreflightContext, f)
			if x.Code != "evidence_binding_refused" {
				t.Fatalf("got %s/%s", x.Outcome, x.Code)
			}
		})
	}
}

func TestContractInspectObservationRules(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		g, c, f, e := completeEvidence(t)
		f.unsupported = true
		if x := Inspect(context.Background(), g, e, c.PreflightContext, f); x.Outcome != effects.Partial {
			t.Fatal(x)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		g, c, f, e := completeEvidence(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if x := Inspect(ctx, g, e, c.PreflightContext, f); x.Code != "authority_refused" {
			t.Fatal(x)
		}
	})
	t.Run("fence", func(t *testing.T) {
		g, c, f, e := completeEvidence(t)
		c.Validate = func(context.Context) error { return fixtureErr }
		if x := Inspect(context.Background(), g, e, c.PreflightContext, f); x.Code != "authority_refused" {
			t.Fatal(x)
		}
	})
	t.Run("clean-complete", func(t *testing.T) {
		g, c, f, e := completeEvidence(t)
		x := Inspect(context.Background(), g, e, c.PreflightContext, f)
		if x.Outcome != effects.AlreadyPresent || len(x.Obligations) != 0 {
			t.Fatal(x)
		}
	})
	for _, field := range []string{"target", "identity", "parent", "not-link"} {
		t.Run("divergent-"+field, func(t *testing.T) {
			g, c, f, e := completeEvidence(t)
			v := f.links["b"]
			switch field {
			case "target":
				v.Target = "/other"
			case "identity":
				v.Identity = "other"
			case "parent":
				v.ParentIdentity = "other"
			case "not-link":
				v.IsLink = false
			}
			f.links["b"] = v
			x := Inspect(context.Background(), g, e, c.PreflightContext, f)
			if x.Inspections[1].State != effects.Divergent || !hasCode(x, "recovery_required") {
				t.Fatalf("%+v", x)
			}
		})
	}
	t.Run("empty-recorded-identity-is-divergent", func(t *testing.T) {
		g, c, f, _ := fixture()
		f.failCreate = 2
		f.failRemove = 1
		r := apply(t, g, c, f) // link a retained with identity, recorded Partial
		_ = r
		e := r.Evidence.Clone()
		e.Links[0].LinkIdentity = ""
		x := Inspect(context.Background(), g, e, c.PreflightContext, f)
		if x.Inspections[0].State != effects.Divergent {
			t.Fatalf("%+v", x.Inspections)
		}
	})
	t.Run("pending-or-omitted-with-existing-destination-is-divergent", func(t *testing.T) {
		g, c, f, _ := fixture()
		f.failCreate = 2
		r := apply(t, g, c, f)
		e := r.Evidence.Clone()
		f.links["c"] = LinkObservation{Exists: true, IsLink: true, Target: "/resource/provider/c", Identity: "z", ParentIdentity: "parent"}
		e.Links[2].Outcome = effects.Pending
		e.Links[2].Source, e.Links[2].Target = "/resource/provider/c", "/resource/provider/c"
		x := Inspect(context.Background(), g, e, c.PreflightContext, f)
		if x.Inspections[2].State != effects.Divergent {
			t.Fatalf("%+v", x.Inspections)
		}
	})
	t.Run("missing-vs-conflict", func(t *testing.T) {
		g, c, f, e := completeEvidence(t)
		delete(f.links, "a")
		x := Inspect(context.Background(), g, e, c.PreflightContext, f)
		if x.Outcome != effects.Partial || x.Code != "evidence_missing" || x.Inspections[0].State != effects.Missing || !hasCode(x, "recovery_required") {
			t.Fatalf("%+v", x)
		}
		v := f.links["b"]
		v.Identity = "other"
		f.links["b"] = v
		x = Inspect(context.Background(), g, e, c.PreflightContext, f)
		if x.Outcome != effects.Partial {
			t.Fatalf("missing overrode divergence: %+v", x)
		}
		delete(f.links, "c") // missing after a divergent one must not downgrade
		x = Inspect(context.Background(), g, e, c.PreflightContext, f)
		if x.Outcome != effects.Partial {
			t.Fatalf("later missing overrode divergence: %+v", x)
		}
	})
	t.Run("source-changed", func(t *testing.T) {
		g, c, f, e := completeEvidence(t)
		f.sources["a"] = false
		x := Inspect(context.Background(), g, e, c.PreflightContext, f)
		if x.Outcome != effects.Partial || x.Code != "source_changed" || !hasCode(x, "recovery_required") {
			t.Fatalf("%+v", x)
		}
		v := f.links["c"]
		v.Identity = "other"
		f.links["c"] = v
		f.sources["b"] = false
		x = Inspect(context.Background(), g, e, c.PreflightContext, f)
		if x.Outcome != effects.Partial || x.Code != "evidence_diverged" {
			t.Fatalf("source change overrode conflict: %+v", x)
		}
	})
	t.Run("interrupted-is-partial-even-when-all-match", func(t *testing.T) {
		g, c, f, e := completeEvidence(t)
		e.Phase = effects.InterruptedPhase
		e.Outcome = effects.Partial
		x := Inspect(context.Background(), g, e, c.PreflightContext, f)
		if x.Outcome != effects.Partial || x.Code != "inspected_interrupted" || !hasCode(x, "recovery_required") {
			t.Fatalf("%+v", x)
		}
	})
	t.Run("omitted-entry-skips-source-check", func(t *testing.T) {
		g, c, f, _ := fixture()
		g.Bindings[2].Required = false
		f.sources["c"] = false
		r := apply(t, g, c, f)
		x := Inspect(context.Background(), g, r.Evidence, c.PreflightContext, f)
		if x.Code != "inspected_complete" {
			t.Fatalf("%+v", x)
		}
	})
}

// ---- additional kills -----------------------------------------------------

func TestContractApplyRevalidatesContext(t *testing.T) {
	cases := map[string]struct {
		mutate func(*effects.ApplyContext)
		want   string
	}{
		"locks-gone":       {func(c *effects.ApplyContext) { c.HeldLocks = nil }, "missing_lock"},
		"header-changed":   {func(c *effects.ApplyContext) { c.Header.OperationID = "other" }, "invalid_binding"},
		"authority-denied": {func(c *effects.ApplyContext) { c.Validate = func(context.Context) error { return fixtureErr } }, "authority_refused"},
		"authority-nil":    {func(c *effects.ApplyContext) { c.Validate = nil }, "missing_authority"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g, c, f, _ := fixture()
			p, _ := Preflight(context.Background(), g, c.PreflightContext, f)
			tc.mutate(&c)
			r := Apply(context.Background(), p, c, f)
			if r.Code != tc.want || f.creates != 0 {
				t.Fatalf("%s creates=%d", r.Code, f.creates)
			}
		})
	}
}

func TestContractUnsortedInputIsDeterministic(t *testing.T) {
	g1, c1, f1, s1 := fixture()
	g2, c2, f2, s2 := fixture()
	g2.Bindings[0], g2.Bindings[2] = g2.Bindings[2], g2.Bindings[0]
	r1 := apply(t, g1, c1, f1)
	r2 := apply(t, g2, c2, f2)
	if fmt.Sprintf("%+v", r1) != fmt.Sprintf("%+v", r2) || fmt.Sprintf("%+v", s1.evidence) != fmt.Sprintf("%+v", s2.evidence) {
		t.Fatalf("order dependent")
	}
	if g2.Bindings[0].Destination != "c" {
		t.Fatal("caller input reordered")
	}
}

func TestContractPortErrorShapes(t *testing.T) {
	t.Run("source-error-with-accessible-observation", func(t *testing.T) {
		g, c, f, _ := fixture()
		pp := &viewPort{fakePort: f, source: func(h ResolvedHome, rel string) (SourceObservation, error) {
			return SourceObservation{LogicalPath: h.LogicalPath + "/" + rel, CanonicalPath: h.CanonicalPath + "/" + rel, Accessible: true}, fixtureErr
		}}
		if got := preflightCode(g, c, pp); got != "source_unavailable" {
			t.Fatal(got)
		}
	})
	t.Run("session-inspect-error", func(t *testing.T) {
		g, c, f, _ := fixture()
		pp := &errInspect{fakePort: f}
		p, _ := Preflight(context.Background(), g, c.PreflightContext, pp)
		r := Apply(context.Background(), p, c, pp)
		if r.Outcome != effects.Conflict || r.Code != "destination_unavailable" || f.creates != 0 {
			t.Fatalf("%s %s creates=%d", r.Outcome, r.Code, f.creates)
		}
	})
	t.Run("open-candidate-error", func(t *testing.T) {
		g, c, f, _ := fixture()
		pp := &openErr{fakePort: f}
		p, _ := Preflight(context.Background(), g, c.PreflightContext, pp)
		r := Apply(context.Background(), p, c, pp)
		if r.Outcome != effects.Conflict || r.Code != "candidate_unavailable" {
			t.Fatalf("%s %s", r.Outcome, r.Code)
		}
	})
}

type errInspect struct{ *fakePort }

func (p *errInspect) OpenCandidate(context.Context, effects.RootInput) (CandidateSession, error) {
	return &errInspectSession{p.fakePort}, nil
}

type errInspectSession struct{ *fakePort }

func (s *errInspectSession) Inspect(context.Context, string) (LinkObservation, error) {
	return LinkObservation{}, fixtureErr
}

type openErr struct{ *fakePort }

func (p *openErr) OpenCandidate(context.Context, effects.RootInput) (CandidateSession, error) {
	return nil, fixtureErr
}

func TestContractSessionViewIsLinkAndOmitted(t *testing.T) {
	t.Run("session-sees-non-link-with-matching-target", func(t *testing.T) {
		g, c, f, _ := fixture()
		pp := &viewPort{fakePort: f, inspect: func(rel string) (LinkObservation, bool) {
			if rel == "b" {
				return LinkObservation{Exists: true, IsLink: false, Target: "/resource/provider/b", Identity: "x", ParentIdentity: "parent"}, true
			}
			return LinkObservation{}, false
		}}
		p, _ := Preflight(context.Background(), g, c.PreflightContext, pp)
		r := Apply(context.Background(), p, c, pp)
		if r.Code != "destination_conflict" || f.creates != 0 {
			t.Fatalf("%s creates=%d", r.Code, f.creates)
		}
	})
	t.Run("session-sees-existing-for-omitted-binding", func(t *testing.T) {
		g, c, f, _ := fixture()
		g.Bindings[2].Required = false
		f.sources["c"] = false
		pp := &viewPort{fakePort: f, inspect: func(rel string) (LinkObservation, bool) {
			if rel == "c" {
				return LinkObservation{Exists: true, IsLink: true, Target: "", Identity: "x", ParentIdentity: "parent"}, true
			}
			return LinkObservation{}, false
		}}
		p, _ := Preflight(context.Background(), g, c.PreflightContext, pp)
		r := Apply(context.Background(), p, c, pp)
		if r.Code != "destination_conflict" || f.creates != 0 {
			t.Fatalf("%s creates=%d", r.Code, f.creates)
		}
	})
	t.Run("preflight-non-link-matching-target", func(t *testing.T) {
		g, c, f, _ := fixture()
		f.links["c"] = LinkObservation{Exists: true, IsLink: false, Target: "/resource/provider/c", Identity: "x", ParentIdentity: "parent"}
		if got := preflightCode(g, c, f); got != "destination_conflict" {
			t.Fatal(got)
		}
	})
}

func TestContractReceiptsDoNotMisreportFailures(t *testing.T) {
	g, c, f, s := fixture()
	f.failCreate = 3
	r := apply(t, g, c, f)
	last := s.evidence[len(s.evidence)-1]
	if r.Evidence.Outcome != effects.Partial || last.Outcome != effects.Partial || last.Phase != effects.InterruptedPhase {
		t.Fatalf("result=%s receipt=%s/%s", r.Evidence.Outcome, last.Phase, last.Outcome)
	}
}

func TestContractInspectBookkeeping(t *testing.T) {
	t.Run("removed-link-reappearing-is-divergent", func(t *testing.T) {
		g, c, f, s := fixture()
		f.failCreate = 3
		r := apply(t, g, c, f)
		if len(f.links) != 0 {
			t.Fatal("setup")
		}
		_ = s
		// the removed link "a" comes back with exactly the recorded identity
		f.links["a"] = LinkObservation{Exists: true, IsLink: true, Target: "/resource/provider/a", Identity: "new-1", ParentIdentity: "parent"}
		x := Inspect(context.Background(), g, r.Evidence, c.PreflightContext, f)
		if x.Inspections[0].State != effects.Divergent || x.Outcome != effects.Partial {
			t.Fatalf("%+v", x)
		}
	})
	t.Run("empty-identity-on-both-sides-is-divergent", func(t *testing.T) {
		g, c, f, e := completeEvidence(t)
		e.Links[0].LinkIdentity = ""
		e.Phase = effects.InterruptedPhase
		e.Outcome = effects.Partial
		e.Links[0].Outcome = effects.Partial
		v := f.links["a"]
		v.Identity = ""
		f.links["a"] = v
		x := Inspect(context.Background(), g, e, c.PreflightContext, f)
		if x.Inspections[0].State != effects.Divergent {
			t.Fatalf("%+v", x.Inspections)
		}
	})
	t.Run("source-change-does-not-override-earlier-divergence", func(t *testing.T) {
		g, c, f, e := completeEvidence(t)
		v := f.links["a"]
		v.Identity = "other"
		f.links["a"] = v
		f.sources["c"] = false
		x := Inspect(context.Background(), g, e, c.PreflightContext, f)
		if x.Outcome != effects.Partial || x.Code != "evidence_diverged" {
			t.Fatalf("%+v", x)
		}
	})
}
