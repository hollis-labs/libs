//go:build linux || darwin

package workspace_test

import (
	"context"
	"errors"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/repositories"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type rootRepositoryPort struct {
	f                              *fixturePorts
	exists, unsupported, uncertain bool
	failObserve                    bool
	beforeCreate                   func()
	head                           string
	observedRequests               []repositories.Request
}

func (p *rootRepositoryPort) Supported(repositories.Request) bool { return !p.unsupported }
func (p *rootRepositoryPort) Source(_ context.Context, r repositories.Request) (repositories.Observation, error) {
	p.f.events = append(p.f.events, "repo:source")
	return repositories.Observation{Exists: true, Path: r.Source.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Head: r.BaseCommit}, nil
}
func (p *rootRepositoryPort) ResolveBase(_ context.Context, r repositories.Request) (string, error) {
	p.f.events = append(p.f.events, "repo:base")
	return r.BaseCommit, nil
}
func (p *rootRepositoryPort) BranchAvailable(context.Context, repositories.Request) error { return nil }
func (p *rootRepositoryPort) Observe(_ context.Context, r repositories.Request) (repositories.Observation, error) {
	p.f.events = append(p.f.events, "repo:observe")
	p.observedRequests = append(p.observedRequests, r)
	if p.failObserve {
		return repositories.Observation{}, errors.New("fixture observation")
	}
	head := p.head
	if head == "" {
		head = r.BaseCommit
	}
	return repositories.Observation{Exists: p.exists, Path: r.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Branch: r.Branch, Head: head, Dirty: p.head != ""}, nil
}
func (p *rootRepositoryPort) Create(ctx context.Context, r repositories.Request, validate func(context.Context) error) (repositories.Observation, bool, error) {
	p.f.events = append(p.f.events, "repo:create")
	if p.beforeCreate != nil {
		p.beforeCreate()
	}
	if err := validate(ctx); err != nil {
		return repositories.Observation{}, false, err
	}
	if err := os.Mkdir(r.Path, 0700); err != nil {
		return repositories.Observation{}, false, err
	}
	p.exists = true
	if p.uncertain {
		return repositories.Observation{}, true, errors.New("uncertain fixture")
	}
	o, e := p.Observe(ctx, r)
	return o, true, e
}
func (*rootRepositoryPort) Safety(context.Context, repositories.Request, effects.AttachmentEvidence) (repositories.Safety, error) {
	panic("root must not retire")
}
func (*rootRepositoryPort) Remove(context.Context, repositories.Request, effects.AttachmentEvidence, func(context.Context) error) (bool, error) {
	panic("root must not retire")
}

func repositoryApplyInputs(t *testing.T) (workspace.Spec, workspace.ResolvedContent, workspace.Resources, *fixturePorts, *effectStore, *rootTrustPort, *rootRepositoryPort) {
	t.Helper()
	s, c, r, f, store, tp := effectApplyInputs(t)
	base := s.Home.Root.AllowedBase
	source := workspace.RootRef{ID: "repo-source", Path: filepath.Join(base, "repo-source"), AllowedBase: base, Owner: "fixture", Provenance: "fixture"}
	common := source
	common.ID = "repo-common"
	common.Path = filepath.Join(source.Path, ".git")
	parent := source
	parent.ID = "repo-parent"
	parent.Path = filepath.Join(base, "repo-parent")
	target := parent
	target.ID = "attachment"
	target.Path = filepath.Join(parent.Path, "attachment")
	for _, root := range []workspace.RootRef{source, common, parent} {
		if err := os.Mkdir(root.Path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, root := range []workspace.RootRef{source, common, parent, target} {
		r.Roots = append(r.Roots, root)
		o, e := workspace.InspectRoot(root)
		if e != nil {
			t.Fatal(e)
		}
		f.observed.Roots = append(f.observed.Roots, o)
	}
	grant := workspace.EffectGrant{Kind: workspace.RepositoryEffect, RootID: parent.ID, AuthorizationID: "repo-authority", Version: "1"}
	s.Effects = append(s.Effects, grant)
	r.Grants = append(r.Grants, grant)
	r.Capabilities = append(r.Capabilities, workspace.RepositoryAttachments)
	f.observed.Capabilities = append(f.observed.Capabilities, workspace.RepositoryAttachments)
	s.Repos = []workspace.RepoSpec{{ID: "repo", Source: workspace.ResourceRef{ID: source.ID, Path: source.Path, Provenance: source.Provenance}, DesiredRoot: target, Mode: workspace.Worktree, BaseCommit: strings.Repeat("a", 40), BranchTemplate: "work/fixture", Retention: workspace.Keep}}
	root := func(v workspace.RootRef) effects.RootInput {
		return effects.RootInput{ID: v.ID, Path: v.Path, AllowedBase: v.AllowedBase, Owner: v.Owner, Provenance: v.Provenance, MutationIdentity: v.Path, Inactive: true, PrivateCustody: true}
	}
	s.EffectInputs.Repositories = []repositories.Request{{Mode: repositories.Worktree, Ownership: repositories.Owned, Source: root(source), Common: root(common), Base: root(parent), Path: target.Path, RepositoryID: "repo", SourceIdentity: "source-id", CommonIdentity: "common-id", BaseCommit: s.Repos[0].BaseCommit, Branch: "work/fixture", CandidateRootID: s.Boot.Candidate.ID, AuthorizationID: grant.AuthorizationID, AuthorizationVersion: grant.Version}}
	return s, c, r, f, store, tp, &rootRepositoryPort{f: f}
}
func repositoryPorts(f *fixturePorts, store *effectStore, tp *rootTrustPort, rp *rootRepositoryPort) workspace.Ports {
	p := effectPorts(f, store, tp)
	p.Repositories = rp
	return p
}

func TestRootRepositoriesPreflightAndOrderedDispatch(t *testing.T) {
	s, c, r, f, store, tp, rp := repositoryApplyInputs(t)
	p := planned(t, s, c, r, f.observed)
	got, err := workspace.Materialize(context.Background(), p, repositoryPorts(f, store, tp, rp))
	if err != nil || !got.ArtifactsComplete() || got.LaunchReady() {
		t.Fatal("repository apply", err, got.Obligations)
	}
	req := p.EffectInputs().Repositories[0]
	for _, root := range []effects.RootInput{req.Source, req.Common, req.Base} {
		if !slices.Contains(p.LockKeys(), workspace.LockKey{r.LockNamespace, root.Path}) {
			t.Fatal("missing lock", root.ID)
		}
	}
	pre := slices.Index(f.events, "repo:observe")
	directory := -1
	for i, e := range f.events {
		if strings.HasPrefix(e, "directory:") {
			directory = i
			break
		}
	}
	if pre < len(p.LockKeys()) || directory <= pre {
		t.Fatal("repository preflight after mutation", f.events)
	}
	cred := slices.Index(f.events, "effect:credential_links:complete")
	create := slices.Index(f.events, "repo:create")
	complete := slices.Index(f.events, "effect:repository_attachment:complete")
	trust := slices.Index(f.events, "trust:begin")
	if cred < 0 || create <= cred || complete <= create || trust <= complete {
		t.Fatal("wrong effect order", f.events)
	}
	if len(got.Receipt.RepositoryRequests) != 1 {
		t.Fatal("lost original bound request")
	}
}
func TestRootRepositoryRefusalBeforeAnyMutation(t *testing.T) {
	for _, name := range []string{"nil", "unsupported", "cancel", "observation"} {
		t.Run(name, func(t *testing.T) {
			s, c, r, f, store, tp, rp := repositoryApplyInputs(t)
			p := planned(t, s, c, r, f.observed)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ports := repositoryPorts(f, store, tp, rp)
			switch name {
			case "nil":
				ports.Repositories = nil
			case "unsupported":
				rp.unsupported = true
			case "cancel":
				cancel()
			case "observation":
				rp.failObserve = true
			}
			got, err := workspace.Materialize(ctx, p, ports)
			if err == nil || got.ArtifactsComplete() || len(store.records) > 0 || len(got.Retained) > 0 {
				t.Fatal("refusal mutated", err)
			}
			if _, err := os.Stat(s.Boot.Candidate.Path); !os.IsNotExist(err) {
				t.Fatal("created candidate", err)
			}
		})
	}
}
func TestRootRepositoryIntentFailureAndUncertainMutation(t *testing.T) {
	for _, name := range []string{"intent", "uncertain", "late-authority"} {
		t.Run(name, func(t *testing.T) {
			s, c, r, f, store, tp, rp := repositoryApplyInputs(t)
			p := planned(t, s, c, r, f.observed)
			switch name {
			case "intent":
				store.fail = func(r workspace.Receipt) bool {
					if len(r.EffectEvidence) == 0 {
						return false
					}
					e := r.EffectEvidence[len(r.EffectEvidence)-1]
					return e.Kind == effects.RepositoryAttachment && e.Phase == effects.IntentPhase
				}
			case "uncertain":
				rp.uncertain = true
			case "late-authority":
				rp.beforeCreate = func() { f.validateErr = errors.New("revoked") }
			}
			got, err := workspace.Materialize(context.Background(), p, repositoryPorts(f, store, tp, rp))
			if err == nil || got.ArtifactsComplete() || got.Status != workspace.Partial || slices.Contains(f.events, "trust:begin") {
				t.Fatal("failed repository reached trust", err, f.events)
			}
			if name == "intent" && slices.Contains(f.events, "repo:create") {
				t.Fatal("create despite failed intent")
			}
			if name == "uncertain" {
				if !slices.Contains(got.Obligations, workspace.Obligation{Kind: workspace.RecoveryInspectionRequired, RootID: "repo-parent", Code: "recovery_required"}) {
					t.Fatal("lost recovery")
				}
				if !slices.Contains(got.Retained, s.Repos[0].DesiredRoot) {
					t.Fatal("lost attachment retention", got.Retained)
				}
			}
		})
	}
}

func refreshRepositoryObservations(t *testing.T, r workspace.Resources, f *fixturePorts) {
	t.Helper()
	f.observed.Roots = nil
	for _, root := range append(slices.Clone(r.Roots), r.LockRoot) {
		o, e := workspace.InspectRoot(root)
		if e != nil {
			t.Fatal(e)
		}
		f.observed.Roots = append(f.observed.Roots, o)
	}
}

func TestRootRepositoryCompletedRecoveryUsesOriginalRequestNoCreation(t *testing.T) {
	s, c, r, f, store, tp, rp := repositoryApplyInputs(t)
	p := planned(t, s, c, r, f.observed)
	first, e := workspace.Materialize(context.Background(), p, repositoryPorts(f, store, tp, rp))
	if e != nil {
		t.Fatal(e)
	}
	original := first.Receipt.RepositoryRequests[0]
	obligation := workspace.Obligation{Kind: workspace.RecoveryInspectionRequired, RootID: "repo-parent", Code: "earlier-retained"}
	first.Receipt.Obligations = append(first.Receipt.Obligations, obligation)
	s.OperationID = "retry-operation"
	s.Boot.CandidateGeneration = first.Handles[0].Manifest.Generation
	r.RecoveryReceipts = []workspace.Receipt{first.Receipt}
	rp.head = strings.Repeat("b", 40)
	refreshRepositoryObservations(t, r, f)
	f.events = nil
	rp.observedRequests = nil
	retry := planned(t, s, c, r, f.observed)
	got, e := workspace.Materialize(context.Background(), retry, repositoryPorts(f, store, tp, rp))
	if e != nil || !got.ArtifactsComplete() || got.LaunchReady() || !slices.Contains(got.Obligations, obligation) {
		t.Fatal("recovery failed/lost obligation", e, got.Obligations)
	}
	if slices.Contains(f.events, "repo:create") || slices.Contains(f.events, "repo:base") {
		t.Fatal("completed attachment recreated/reinterpreted", f.events)
	}
	if len(rp.observedRequests) != 1 || rp.observedRequests[0] != original {
		t.Fatal("inspection not original bound request", rp.observedRequests)
	}
	if _, e := os.Stat(original.Path); e != nil {
		t.Fatal("advanced dirty attachment lost", e)
	}
}

func TestRootRepositoryIntentAndInterruptedRecoveryNeverReplay(t *testing.T) {
	for _, phase := range []effects.Phase{effects.IntentPhase, effects.InterruptedPhase} {
		t.Run(string(phase), func(t *testing.T) {
			s, c, r, f, store, tp, rp := repositoryApplyInputs(t)
			p := planned(t, s, c, r, f.observed)
			rp.uncertain = true
			first, e := workspace.Materialize(context.Background(), p, repositoryPorts(f, store, tp, rp))
			if e == nil {
				t.Fatal("fixture uncertainty missing")
			}
			var evidence effects.Evidence
			for _, ev := range first.Receipt.EffectEvidence {
				if ev.Kind == effects.RepositoryAttachment {
					evidence = ev
				}
			}
			evidence.Phase = phase
			evidence.Outcome = effects.Partial
			if phase == effects.IntentPhase {
				evidence.Outcome = effects.Pending
			}
			evidence.Attachments[0].Outcome = evidence.Outcome
			evidence.Attachments[0].Created = false
			first.Receipt.EffectEvidence = []effects.Evidence{evidence}
			first.Receipt.Obligations = nil // intent itself must retain, without an explicit obligation
			s.OperationID = "uncertain-retry"
			s.Boot.CandidateGeneration = first.Handles[0].Manifest.Generation
			r.RecoveryReceipts = []workspace.Receipt{first.Receipt}
			rp.uncertain = false
			refreshRepositoryObservations(t, r, f)
			f.events = nil
			store.records = nil
			retry := planned(t, s, c, r, f.observed)
			got, e := workspace.Materialize(context.Background(), retry, repositoryPorts(f, store, tp, rp))
			if e == nil || got.Status != workspace.Partial || got.ArtifactsComplete() || len(store.records) > 0 || slices.Contains(f.events, "repo:create") || slices.Contains(f.events, "trust:begin") {
				t.Fatal("uncertain replay/mutation", e, f.events)
			}
			if !slices.Contains(got.Obligations, workspace.Obligation{Kind: workspace.RecoveryInspectionRequired, RootID: "repo-parent", Code: "recovery_required"}) || !slices.Contains(got.Retained, s.Repos[0].DesiredRoot) {
				t.Fatal("lost uncertain attachment", got.Obligations, got.Retained)
			}
		})
	}
}

func TestRootRepositoryRecoveryRejectsForeignBindings(t *testing.T) {
	for _, name := range []string{"no-original", "foreign-header", "foreign-grant", "foreign-kind", "foreign-request", "foreign-original", "late-cancel"} {
		t.Run(name, func(t *testing.T) {
			s, c, r, f, store, tp, rp := repositoryApplyInputs(t)
			p := planned(t, s, c, r, f.observed)
			first, e := workspace.Materialize(context.Background(), p, repositoryPorts(f, store, tp, rp))
			if e != nil {
				t.Fatal(e)
			}
			var evidence effects.Evidence
			for _, ev := range first.Receipt.EffectEvidence {
				if ev.Kind == effects.RepositoryAttachment {
					evidence = ev
				}
			}
			first.Receipt.EffectEvidence = []effects.Evidence{evidence}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "no-original":
				first.Receipt.RepositoryRequests = nil
			case "foreign-header":
				first.Receipt.EffectEvidence[0].Header.OperationID = "foreign"
			case "foreign-grant":
				first.Receipt.EffectEvidence[0].Attachments[0].AuthorizationVersion = "foreign"
			case "foreign-kind":
				first.Receipt.EffectEvidence[0].Links = []effects.LinkEvidence{{Source: "foreign"}}
			case "foreign-original":
				first.Receipt.RepositoryRequests[0].SourceIdentity = "foreign"
				first.Receipt.EffectEvidence[0].Attachments[0].SourceIdentity = "foreign"
			case "foreign-request":
				first.Receipt.RepositoryRequests[0].Base.PrivateCustody = false
			case "late-cancel":
				cancel()
			}
			s.OperationID = "foreign-retry"
			s.Boot.CandidateGeneration = first.Handles[0].Manifest.Generation
			r.RecoveryReceipts = []workspace.Receipt{first.Receipt}
			refreshRepositoryObservations(t, r, f)
			f.events = nil
			store.records = nil
			retry := planned(t, s, c, r, f.observed)
			got, e := workspace.Materialize(ctx, retry, repositoryPorts(f, store, tp, rp))
			if e == nil || got.ArtifactsComplete() || len(store.records) > 0 || slices.Contains(f.events, "repo:create") {
				t.Fatal("foreign recovery accepted", e)
			}
		})
	}
}

func TestRootRepositoryLastGroupPreflightStopsEveryMutation(t *testing.T) {
	s, c, r, f, store, tp, rp := repositoryApplyInputs(t)
	tp.unsupported = true
	p := planned(t, s, c, r, f.observed)
	got, e := workspace.Materialize(context.Background(), p, repositoryPorts(f, store, tp, rp))
	if e == nil || len(store.records) > 0 || got.ArtifactsComplete() || slices.Contains(f.events, "repo:create") {
		t.Fatal("late preflight crossed boundary", e)
	}
	if _, e := os.Stat(s.Boot.Candidate.Path); !os.IsNotExist(e) {
		t.Fatal("artifact mutation before all preflights")
	}
}

func TestRootRepositoryFinalReceiptFailureRetainsAttachment(t *testing.T) {
	s, c, r, f, store, tp, rp := repositoryApplyInputs(t)
	p := planned(t, s, c, r, f.observed)
	store.fail = func(r workspace.Receipt) bool {
		if len(r.EffectEvidence) == 0 {
			return false
		}
		e := r.EffectEvidence[len(r.EffectEvidence)-1]
		return e.Kind == effects.RepositoryAttachment && e.Phase == effects.CompletePhase
	}
	got, e := workspace.Materialize(context.Background(), p, repositoryPorts(f, store, tp, rp))
	if e == nil || got.ArtifactsComplete() || !slices.Contains(got.Retained, s.Repos[0].DesiredRoot) || slices.Contains(f.events, "trust:begin") {
		t.Fatal("final receipt uncertainty lost attachment", e, got.Retained)
	}
	if !slices.Contains(got.Obligations, workspace.Obligation{Kind: workspace.RecoveryInspectionRequired, RootID: "repo-parent", Code: "receipt_pending"}) {
		t.Fatal("lost receipt uncertainty", got.Obligations)
	}
}

func TestRootRepositoryExistingPoliciesRequireExplicitAuthority(t *testing.T) {
	for _, mode := range []workspace.RepoMode{workspace.Checkout, workspace.Readonly} {
		t.Run(string(mode), func(t *testing.T) {
			s, c, r, f, store, tp, rp := repositoryApplyInputs(t)
			req := &s.EffectInputs.Repositories[0]
			req.Existing = true
			req.Ownership = repositories.UserOwned
			req.Mode = repositories.Mode(mode)
			s.Repos[0].Mode = mode
			s.Repos[0].Existing = &workspace.AttachmentReceipt{RepositoryID: "repo", Path: req.Path, Mode: mode}
			if mode == workspace.Checkout {
				req.UserWriteAuthorizationID = "user-write"
				req.UserWriteAuthorizationVersion = "1"
				g := workspace.EffectGrant{Kind: workspace.RepositoryEffect, RootID: s.Repos[0].DesiredRoot.ID, AuthorizationID: "user-write", Version: "1"}
				s.Effects = append(s.Effects, g)
				r.Grants = append(r.Grants, g)
			} else {
				req.Readonly = repositories.ReadonlyEvidence{Path: req.Path, CommonPath: req.Common.Path, ID: "readonly-proof", Revision: "1", Provenance: "fixture", Enforced: true}
			}
			if e := os.Mkdir(req.Path, 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(req.Path, "operator.txt"), []byte("operator bytes"), 0600); e != nil {
				t.Fatal(e)
			}
			rp.exists = true
			rp.head = strings.Repeat("c", 40)
			refreshRepositoryObservations(t, r, f)
			p := planned(t, s, c, r, f.observed)
			got, e := workspace.Materialize(context.Background(), p, repositoryPorts(f, store, tp, rp))
			if e != nil || !got.ArtifactsComplete() || slices.Contains(f.events, "repo:create") {
				t.Fatal("existing policy failed", e, f.events)
			}
			data, e := os.ReadFile(filepath.Join(req.Path, "operator.txt"))
			if e != nil || string(data) != "operator bytes" {
				t.Fatal("operator data modified")
			}
			if mode == workspace.Checkout {
				req.UserWriteAuthorizationVersion = "foreign"
			} else {
				req.Readonly.Enforced = false
			}
			if _, e := workspace.Plan(s, c, r, f.observed); e == nil {
				t.Fatal("missing independent authority accepted")
			}
		})
	}
}

func TestRootRepositoryCancelDuringCreateRetainsCompletedEarlierEffects(t *testing.T) {
	s, c, r, f, store, tp, rp := repositoryApplyInputs(t)
	p := planned(t, s, c, r, f.observed)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.beforeCreate = cancel
	got, e := workspace.Materialize(ctx, p, repositoryPorts(f, store, tp, rp))
	if e == nil || got.ArtifactsComplete() || got.Status != workspace.Partial || slices.Contains(f.events, "trust:begin") {
		t.Fatal("cancelled repository reached trust", e, f.events)
	}
	if _, e := os.Readlink(filepath.Join(s.Boot.Candidate.Path, "auth.json")); e != nil {
		t.Fatal("earlier credential lost", e)
	}
	releases := []string{}
	acquires := []string{}
	for _, event := range f.events {
		if strings.HasPrefix(event, "acquire:") {
			acquires = append(acquires, strings.TrimPrefix(event, "acquire:"))
		}
		if strings.HasPrefix(event, "release:") {
			releases = append(releases, strings.TrimPrefix(event, "release:"))
		}
	}
	slices.Reverse(acquires)
	if !slices.Equal(releases, acquires) {
		t.Fatal("lock release order", f.events)
	}
	if _, e := os.Stat(s.Repos[0].DesiredRoot.Path); !os.IsNotExist(e) {
		t.Fatal("cancelled creation mutated")
	}
}
