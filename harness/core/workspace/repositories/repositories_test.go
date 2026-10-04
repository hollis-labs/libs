package repositories

import (
	"context"
	"errors"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"testing"
)

const baseCommit = "1111111111111111111111111111111111111111"
const advancedHead = "2222222222222222222222222222222222222222"

var errFixture = errors.New("fixture operation failed")

type fakePort struct {
	source, observed      Observation
	safety                Safety
	creates, removes      int
	createErr, observeErr error
	uncertain             bool
	branchErr             bool
	supportCheckout       bool
}

func (p *fakePort) Supported(r Request) bool {
	return r.Mode == Worktree || r.Mode == Readonly || r.Mode == Checkout && (r.Existing || p.supportCheckout)
}
func (p *fakePort) Source(context.Context, Request) (Observation, error) { return p.source, nil }
func (p *fakePort) ResolveBase(_ context.Context, r Request) (string, error) {
	return r.BaseCommit, nil
}
func (p *fakePort) BranchAvailable(context.Context, Request) error {
	if p.branchErr {
		return errFixture
	}
	return nil
}
func (p *fakePort) Observe(context.Context, Request) (Observation, error) {
	return p.observed, p.observeErr
}
func (p *fakePort) Create(ctx context.Context, r Request, v func(context.Context) error) (Observation, bool, error) {
	if ctx.Err() != nil || v(ctx) != nil {
		return Observation{}, false, errFixture
	}
	p.creates++
	if p.createErr != nil {
		return Observation{}, p.uncertain, p.createErr
	}
	p.observed = Observation{Exists: true, Path: r.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Branch: r.Branch, Head: r.BaseCommit}
	return p.observed, true, nil
}
func (p *fakePort) Safety(context.Context, Request, effects.AttachmentEvidence) (Safety, error) {
	return p.safety, nil
}
func (p *fakePort) Remove(ctx context.Context, r Request, a effects.AttachmentEvidence, v func(context.Context) error) (bool, error) {
	if ctx.Err() != nil || v(ctx) != nil {
		return false, errFixture
	}
	p.removes++
	p.observed.Exists = false
	return true, nil
}

type fakeSink struct {
	rows []effects.Evidence
	hook func(effects.Evidence) error
}

func (s *fakeSink) Record(_ context.Context, e effects.Evidence) error {
	s.rows = append(s.rows, e.Clone())
	if s.hook != nil {
		return s.hook(e)
	}
	return nil
}
func fixture() (Request, effects.ApplyContext, *fakePort, *fakeSink) {
	h := effects.Header{Version: effects.SchemaVersion, OperationID: "operation", InputDigest: "input"}
	root := func(id, path string) effects.RootInput {
		return effects.RootInput{ID: id, Path: path, AllowedBase: "/resource", Owner: "owner", Provenance: "capture", MutationIdentity: path, PrivateCustody: true}
	}
	r := Request{Header: h, Mode: Worktree, Ownership: Owned, Source: root("source", "/resource/source"), Common: root("common", "/resource/source/.git"), Base: root("base", "/resource/attachments"), Path: "/resource/attachments/work", RepositoryID: "repo", SourceIdentity: "source-identity", CommonIdentity: "common-identity", Branch: "work/fixture", BaseCommit: baseCommit, AuthorizationID: "grant", AuthorizationVersion: "revision", CandidateRootID: "candidate"}
	c := effects.ApplyContext{PreflightContext: effects.PreflightContext{Header: h, Validate: func(context.Context) error { return nil }, HeldLocks: []effects.LockIdentity{{Namespace: "/locks", CanonicalID: r.Source.Path}, {Namespace: "/locks", CanonicalID: r.Common.Path}, {Namespace: "/locks", CanonicalID: r.Base.Path}}}, ArtifactRootID: "candidate", ArtifactGeneration: "generation"}
	p := &fakePort{source: Observation{Exists: true, Path: r.Source.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Head: baseCommit}, safety: Safety{Complete: true, Head: baseCommit}}
	s := &fakeSink{}
	c.Receipts = s
	return r, c, p, s
}
func apply(t *testing.T, r Request, c effects.ApplyContext, p Port) effects.Result {
	t.Helper()
	prep, pre := Preflight(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	return Apply(context.Background(), prep, c, p)
}
func TestCreationAndDirtyAdvancedResume(t *testing.T) {
	r, c, p, _ := fixture()
	got := apply(t, r, c, p)
	if got.Outcome != effects.Applied || p.creates != 1 {
		t.Fatal(got)
	}
	p.observed.Head = advancedHead
	p.observed.Dirty = true
	resumed := InspectResume(context.Background(), r, c.PreflightContext, p, got.Evidence)
	if resumed.Outcome != effects.AlreadyPresent || p.creates != 1 || p.observed.Head != advancedHead || !p.observed.Dirty {
		t.Fatal(resumed)
	}
	p.observed.Branch = "other"
	if InspectResume(context.Background(), r, c.PreflightContext, p, got.Evidence).Outcome != effects.Conflict {
		t.Fatal("branch mismatch reused")
	}
	p.observed.Exists = false
	if InspectResume(context.Background(), r, c.PreflightContext, p, got.Evidence).Outcome != effects.Conflict {
		t.Fatal("missing attachment replayed")
	}
}
func TestForeignBindingsAndCollisionRefused(t *testing.T) {
	for _, mutate := range []func(*Request, *effects.ApplyContext, *fakePort){func(r *Request, c *effects.ApplyContext, p *fakePort) { r.Header.InputDigest = "foreign" }, func(r *Request, c *effects.ApplyContext, p *fakePort) { r.Header.Version = "unknown" }, func(r *Request, c *effects.ApplyContext, p *fakePort) { p.source.SourceIdentity = "other" }, func(r *Request, c *effects.ApplyContext, p *fakePort) { r.AuthorizationID = "" }, func(r *Request, c *effects.ApplyContext, p *fakePort) { r.Branch = "" }, func(r *Request, c *effects.ApplyContext, p *fakePort) { r.Branch = "${unresolved}" }, func(r *Request, c *effects.ApplyContext, p *fakePort) { r.BaseCommit = "main" }, func(r *Request, c *effects.ApplyContext, p *fakePort) { r.Base.PrivateCustody = false }, func(r *Request, c *effects.ApplyContext, p *fakePort) { c.HeldLocks = c.HeldLocks[:2] }, func(r *Request, c *effects.ApplyContext, p *fakePort) { c.HeldLocks[0].Namespace = r.Base.Path }, func(r *Request, c *effects.ApplyContext, p *fakePort) { p.source.CommonIdentity = "other" }, func(r *Request, c *effects.ApplyContext, p *fakePort) { p.branchErr = true }, func(r *Request, c *effects.ApplyContext, p *fakePort) { p.observed.Exists = true }, func(r *Request, c *effects.ApplyContext, p *fakePort) { r.Path = r.Base.Path + "/missing/work" }} {
		r, c, p, _ := fixture()
		mutate(&r, &c, p)
		_, got := Preflight(context.Background(), r, c.PreflightContext, p)
		if got.Outcome != effects.Refused || p.creates != 0 {
			t.Fatal(got)
		}
	}
}
func TestPrivateCheckoutUnsupportedAndUserGrantRequired(t *testing.T) {
	r, c, p, _ := fixture()
	r.Mode = Checkout
	if _, got := Preflight(context.Background(), r, c.PreflightContext, p); got.Outcome != effects.Unsupported {
		t.Fatal(got)
	}
	p.supportCheckout = true
	r.Existing = true
	r.Branch = ""
	r.Ownership = UserOwned
	p.observed = Observation{Exists: true, Path: r.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Branch: "main", Head: baseCommit}
	if _, got := Preflight(context.Background(), r, c.PreflightContext, p); got.Outcome != effects.Refused {
		t.Fatal(got)
	}
	r.UserWriteAuthorizationID = "write-grant"
	r.UserWriteAuthorizationVersion = "write-revision"
	for _, field := range []string{"id", "version"} {
		other := r
		if field == "id" {
			other.UserWriteAuthorizationID = ""
		} else {
			other.UserWriteAuthorizationVersion = ""
		}
		if _, got := Preflight(context.Background(), other, c.PreflightContext, p); got.Outcome != effects.Refused {
			t.Fatal("missing write grant component accepted", field, got)
		}
	}
	got := apply(t, r, c, p)
	if got.Outcome != effects.AlreadyPresent || p.creates != 0 {
		t.Fatal(got)
	}
}
func TestReadonlyNeedsEnforcedProof(t *testing.T) {
	r, c, p, _ := fixture()
	r.Mode = Readonly
	r.Existing = true
	r.Branch = ""
	r.Ownership = UserOwned
	p.observed = Observation{Exists: true, Path: r.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Head: baseCommit}
	if _, got := Preflight(context.Background(), r, c.PreflightContext, p); got.Outcome != effects.Refused {
		t.Fatal(got)
	}
	r.Readonly = ReadonlyEvidence{Path: r.Path, CommonPath: r.Common.Path, ID: "sandbox", Revision: "revision", Provenance: "host", Enforced: false}
	if _, got := Preflight(context.Background(), r, c.PreflightContext, p); got.Outcome != effects.Refused {
		t.Fatal("unenforced readonly accepted", got)
	}
	r.Readonly.Enforced = true
	if got := apply(t, r, c, p); got.Outcome != effects.AlreadyPresent || p.creates != 0 {
		t.Fatal(got)
	}
}
func TestIntentBoundaryAndUncertainCreation(t *testing.T) {
	for _, change := range []string{"authority", "collision", "source", "receipt"} {
		t.Run(change, func(t *testing.T) {
			r, c, p, s := fixture()
			prep, _ := Preflight(context.Background(), r, c.PreflightContext, p)
			revoked := false
			c.Validate = func(context.Context) error {
				if revoked {
					return errFixture
				}
				return nil
			}
			s.hook = func(e effects.Evidence) error {
				if e.Phase != effects.IntentPhase {
					return nil
				}
				switch change {
				case "authority":
					revoked = true
				case "collision":
					p.observed.Exists = true
				case "source":
					p.source.CommonIdentity = "other"
				case "receipt":
					return errFixture
				}
				return nil
			}
			got := Apply(context.Background(), prep, c, p)
			if got.Outcome != effects.Refused || p.creates != 0 || got.Evidence.Phase != effects.AbortedPhase {
				t.Fatal(got, p.creates)
			}
		})
	}
	r, c, p, _ := fixture()
	p.createErr = errFixture
	p.uncertain = true
	got := apply(t, r, c, p)
	if got.Outcome != effects.Partial || len(got.Obligations) == 0 || p.removes != 0 {
		t.Fatal(got)
	}
	p.observed = Observation{Exists: true, Path: r.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Branch: r.Branch, Head: r.BaseCommit}
	if InspectResume(context.Background(), r, c.PreflightContext, p, got.Evidence).Outcome != effects.Partial {
		t.Fatal("visible attachment erased uncertainty")
	}
}
func TestReceiptBindingAndClone(t *testing.T) {
	r, c, p, s := fixture()
	got := apply(t, r, c, p)
	for _, mutate := range []func(*effects.Evidence){func(e *effects.Evidence) { e.Header.InputDigest = "other" }, func(e *effects.Evidence) { e.Attachments[0].AuthorizationID = "other" }, func(e *effects.Evidence) { e.Attachments[0].Ownership = string(UserOwned) }, func(e *effects.Evidence) { e.Attachments[0].BaseCommit = advancedHead }, func(e *effects.Evidence) { e.Attachments[0].CommonIdentity = "other" }, func(e *effects.Evidence) { e.Attachments[0].Created = false }} {
		e := got.Evidence.Clone()
		mutate(&e)
		if InspectResume(context.Background(), r, c.PreflightContext, p, e).Outcome != effects.Refused {
			t.Fatal("foreign evidence accepted", e)
		}
	}
	got.Evidence.Attachments[0].Path = "changed"
	if s.rows[len(s.rows)-1].Attachments[0].Path == "changed" {
		t.Fatal("receipt aliased")
	}
}
func retirementFixture(t *testing.T) (Retirement, effects.ApplyContext, *fakePort) {
	t.Helper()
	r, c, p, _ := fixture()
	got := apply(t, r, c, p)
	ret := Retirement{Header: effects.Header{Version: effects.SchemaVersion, OperationID: "retirement", InputDigest: "retirement-input"}, Attachment: r, Receipt: got.Evidence, AcceptedHead: baseCommit, ShippedProofID: "shipped", ShippedProofRevision: "revision", UnusedProofID: "unused", UnusedProofRevision: "revision", Provenance: "host", AuthorizationID: "retire-grant", AuthorizationVersion: "revision"}
	c.Header = ret.Header
	c.Receipts = &fakeSink{}
	return ret, c, p
}
func TestRetirementRetainsUnsafeAndUnknownWork(t *testing.T) {
	for _, mutate := range []func(*Safety){func(s *Safety) { s.Complete = false }, func(s *Safety) { s.Dirty = true }, func(s *Safety) { s.Untracked = true }, func(s *Safety) { s.Ignored = true }, func(s *Safety) { s.Unknown = true }, func(s *Safety) { s.Locked = true }, func(s *Safety) { s.Unreachable = 1 }, func(s *Safety) { s.Ahead = 1 }, func(s *Safety) { s.Ahead = -1 }, func(s *Safety) { s.Head = advancedHead }} {
		r, c, p := retirementFixture(t)
		mutate(&p.safety)
		_, got := CheckRetirement(context.Background(), r, c.PreflightContext, p)
		if got.Outcome != effects.Conflict || p.removes != 0 {
			t.Fatal(got)
		}
	}
}
func TestRetirementRechecksAfterIntentAndNeverDeletesUserCheckout(t *testing.T) {
	r, c, p := retirementFixture(t)
	ticket, pre := CheckRetirement(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	c.Receipts = &fakeSink{hook: func(e effects.Evidence) error {
		if e.Phase == effects.IntentPhase {
			p.safety.Ignored = true
		}
		return nil
	}}
	got := Retire(context.Background(), ticket, c, p)
	if got.Outcome != effects.Conflict || p.removes != 0 {
		t.Fatal(got)
	}
	r, c, p = retirementFixture(t)
	r.Attachment.Ownership = UserOwned
	if _, got := CheckRetirement(context.Background(), r, c.PreflightContext, p); got.Outcome != effects.Refused {
		t.Fatal(got)
	}
	r, c, p = retirementFixture(t)
	ticket, pre = CheckRetirement(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	got = Retire(context.Background(), ticket, c, p)
	if got.Outcome != effects.Removed || p.removes != 1 || p.observed.Exists {
		t.Fatal(got)
	}
}

func TestExistingReadonlyCanAttachSourceItself(t *testing.T) {
	r, c, p, _ := fixture()
	r.Mode = Readonly
	r.Ownership = UserOwned
	r.Existing = true
	r.Branch = ""
	r.Path = r.Source.Path
	r.Base.Path = "/resource"
	r.Base.MutationIdentity = r.Base.Path
	c.HeldLocks[2].CanonicalID = r.Base.Path
	r.Readonly = ReadonlyEvidence{Path: r.Path, CommonPath: r.Common.Path, ID: "sandbox", Revision: "revision", Provenance: "host", Enforced: true}
	p.observed = p.source
	got := apply(t, r, c, p)
	if got.Outcome != effects.AlreadyPresent || p.creates != 0 {
		t.Fatal(got)
	}
}
func TestCancellationAfterAuthorityCallbackCannotCompleteExistingAttachment(t *testing.T) {
	r, c, p, _ := fixture()
	r.Mode = Checkout
	r.Ownership = UserOwned
	r.Existing = true
	r.Branch = ""
	r.UserWriteAuthorizationID = "write-grant"
	r.UserWriteAuthorizationVersion = "revision"
	p.observed = Observation{Exists: true, Path: r.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Head: baseCommit}
	prep, pre := Preflight(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Validate = func(context.Context) error { cancel(); return nil }
	got := Apply(ctx, prep, c, p)
	if got.Outcome != effects.Refused || got.Evidence.Phase != effects.AbortedPhase || p.creates != 0 {
		t.Fatal(got)
	}
}

func TestRetirementInspectionNeverReplaysAndBindsProofs(t *testing.T) {
	r, c, p := retirementFixture(t)
	ticket, pre := CheckRetirement(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	out := Retire(context.Background(), ticket, c, p)
	if out.Outcome != effects.Removed {
		t.Fatal(out)
	}
	inspected := InspectRetirement(context.Background(), r, c.PreflightContext, p, out.Evidence)
	if inspected.Outcome != effects.Removed || p.removes != 1 {
		t.Fatal(inspected)
	}
	interrupted := out.Evidence.Clone()
	interrupted.Phase = effects.InterruptedPhase
	interrupted.Outcome = effects.Partial
	interrupted.Attachments[0].Outcome = effects.Partial
	inspected = InspectRetirement(context.Background(), r, c.PreflightContext, p, interrupted)
	if inspected.Outcome != effects.Partial || len(inspected.Obligations) == 0 || p.removes != 1 {
		t.Fatal(inspected)
	}
	for _, mutate := range []func(*effects.Evidence){func(e *effects.Evidence) { e.Attachments[0].OriginHeader.InputDigest = "foreign" }, func(e *effects.Evidence) { e.Attachments[0].ShippedProofID = "foreign" }, func(e *effects.Evidence) { e.Attachments[0].UnusedProofRevision = "foreign" }, func(e *effects.Evidence) { e.Attachments[0].AuthorizationID = "foreign" }, func(e *effects.Evidence) { e.Attachments[0].SafetyComplete = false }} {
		e := out.Evidence.Clone()
		mutate(&e)
		if got := InspectRetirement(context.Background(), r, c.PreflightContext, p, e); got.Outcome != effects.Refused || p.removes != 1 {
			t.Fatal(got)
		}
	}
}

func TestExistingAttachmentExplicitBranchCannotBeSubstituted(t *testing.T) {
	for _, mode := range []Mode{Checkout, Readonly} {
		r, c, p, _ := fixture()
		r.Mode = mode
		r.Existing = true
		r.Ownership = UserOwned
		r.UserWriteAuthorizationID = "write-grant"
		r.UserWriteAuthorizationVersion = "revision"
		r.Readonly = ReadonlyEvidence{Path: r.Path, CommonPath: r.Common.Path, ID: "sandbox", Revision: "revision", Provenance: "host", Enforced: true}
		p.observed = Observation{Exists: true, Path: r.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Branch: "main", Head: baseCommit}
		r.Branch = "other"
		if _, got := Preflight(context.Background(), r, c.PreflightContext, p); got.Outcome != effects.Refused || p.creates != 0 {
			t.Fatal("explicit branch silently substituted", got)
		}
		r.Branch = "main"
		if got := apply(t, r, c, p); got.Outcome != effects.AlreadyPresent || p.creates != 0 {
			t.Fatal(got)
		}
	}
}

func TestRetirementNeedsEveryIndependentProof(t *testing.T) {
	for _, field := range []string{"authorization", "authorization-version", "shipped", "shipped-version", "unused", "unused-version", "provenance", "accepted-head"} {
		r, c, p := retirementFixture(t)
		switch field {
		case "authorization":
			r.AuthorizationID = ""
		case "authorization-version":
			r.AuthorizationVersion = ""
		case "shipped":
			r.ShippedProofID = ""
		case "shipped-version":
			r.ShippedProofRevision = ""
		case "unused":
			r.UnusedProofID = ""
		case "unused-version":
			r.UnusedProofRevision = ""
		case "provenance":
			r.Provenance = ""
		case "accepted-head":
			r.AcceptedHead = "main"
		}
		if _, got := CheckRetirement(context.Background(), r, c.PreflightContext, p); got.Outcome != effects.Refused || p.removes != 0 {
			t.Fatal(field, got)
		}
	}
}
func TestExistingGrantAndReadonlyProofReceiptsCannotBeSubstituted(t *testing.T) {
	r, c, p, _ := fixture()
	r.Mode = Checkout
	r.Existing = true
	r.Ownership = UserOwned
	r.Branch = "main"
	r.UserWriteAuthorizationID = "write-grant"
	r.UserWriteAuthorizationVersion = "revision"
	p.observed = Observation{Exists: true, Path: r.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Branch: r.Branch, Head: baseCommit}
	out := apply(t, r, c, p)
	for _, mutate := range []func(*effects.AttachmentEvidence){func(e *effects.AttachmentEvidence) { e.UserWriteAuthorizationID = "other" }, func(e *effects.AttachmentEvidence) { e.UserWriteAuthorizationVersion = "other" }, func(e *effects.AttachmentEvidence) { e.ReadonlyProofID = "other" }, func(e *effects.AttachmentEvidence) { e.Owner = "other" }} {
		e := out.Evidence.Clone()
		mutate(&e.Attachments[0])
		if got := InspectResume(context.Background(), r, c.PreflightContext, p, e); got.Outcome != effects.Refused {
			t.Fatal(got)
		}
	}
}
func TestArtifactAndPersistenceBoundaries(t *testing.T) {
	for _, field := range []string{"root", "generation", "sink"} {
		r, c, p, _ := fixture()
		prepared, pre := Preflight(context.Background(), r, c.PreflightContext, p)
		if pre.Outcome != effects.Prepared {
			t.Fatal(pre)
		}
		switch field {
		case "root":
			c.ArtifactRootID = "other"
		case "generation":
			c.ArtifactGeneration = ""
		case "sink":
			c.Receipts = nil
		}
		if got := Apply(context.Background(), prepared, c, p); got.Outcome != effects.Refused || p.creates != 0 {
			t.Fatal(field, got)
		}
	}
	r, c, p, s := fixture()
	s.hook = func(e effects.Evidence) error {
		if e.Phase == effects.CompletePhase {
			return errFixture
		}
		return nil
	}
	got := apply(t, r, c, p)
	if got.Outcome != effects.Partial || len(got.Obligations) == 0 || !p.observed.Exists || p.removes != 0 {
		t.Fatal(got)
	}
}

func TestGrantVersionRequiredBeforePreflight(t *testing.T) {
	r, c, p, _ := fixture()
	r.AuthorizationVersion = ""
	if _, got := Preflight(context.Background(), r, c.PreflightContext, p); got.Outcome != effects.Refused || p.creates != 0 {
		t.Fatal(got)
	}
}
func TestReceiptGrantAndResourceFieldsCannotBeSubstituted(t *testing.T) {
	r, c, p, _ := fixture()
	out := apply(t, r, c, p)
	cases := map[string]func(*effects.AttachmentEvidence){
		"grant version":       func(a *effects.AttachmentEvidence) { a.AuthorizationVersion = "foreign" },
		"common metadata":     func(a *effects.AttachmentEvidence) { a.CommonPath = "/foreign/metadata" },
		"readonly revision":   func(a *effects.AttachmentEvidence) { a.ReadonlyProofRevision = "foreign" },
		"readonly provenance": func(a *effects.AttachmentEvidence) { a.ReadonlyProvenance = "foreign" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			e := out.Evidence.Clone()
			change(&e.Attachments[0])
			if got := InspectResume(context.Background(), r, c.PreflightContext, p, e); got.Outcome != effects.Refused {
				t.Fatal(got)
			}
			if p.creates != 1 || p.removes != 0 {
				t.Fatal("inspection mutated repository")
			}
		})
	}
}
func TestResumeCancellationDuringAuthorityCallbackRetainsUncertainty(t *testing.T) {
	r, c, p, _ := fixture()
	out := apply(t, r, c, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Validate = func(context.Context) error { cancel(); return nil }
	got := InspectResume(ctx, r, c.PreflightContext, p, out.Evidence)
	if got.Outcome != effects.Partial || len(got.Obligations) == 0 {
		t.Fatal(got)
	}
}

type callbackPort struct {
	*fakePort
	afterObserve func()
	afterSafety  func()
}

func (p *callbackPort) Observe(ctx context.Context, r Request) (Observation, error) {
	o, e := p.fakePort.Observe(ctx, r)
	if p.afterObserve != nil {
		p.afterObserve()
	}
	return o, e
}
func (p *callbackPort) Safety(ctx context.Context, r Request, a effects.AttachmentEvidence) (Safety, error) {
	o, e := p.fakePort.Safety(ctx, r, a)
	if p.afterSafety != nil {
		p.afterSafety()
	}
	return o, e
}
func TestResumeObserveCancellationRetains(t *testing.T) {
	r, c, p, _ := fixture()
	done := apply(t, r, c, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := &callbackPort{fakePort: p, afterObserve: cancel}
	got := InspectResume(ctx, r, c.PreflightContext, port, done.Evidence)
	if got.Outcome != effects.Partial || len(got.Obligations) == 0 {
		t.Fatalf("cancelled observation lost uncertainty: outcome=%s obligations=%v", got.Outcome, got.Obligations)
	}
}
func TestRetirementObserveCancellationRetains(t *testing.T) {
	r, c, p := retirementFixture(t)
	ticket, pre := CheckRetirement(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	done := Retire(context.Background(), ticket, c, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := &callbackPort{fakePort: p, afterObserve: cancel}
	got := InspectRetirement(ctx, r, c.PreflightContext, port, done.Evidence)
	if got.Outcome != effects.Partial || len(got.Obligations) == 0 {
		t.Fatalf("cancelled retirement observation lost uncertainty: outcome=%s obligations=%v", got.Outcome, got.Obligations)
	}
}
func TestFinalExistingObservationRevocation(t *testing.T) {
	r, c, p, s := fixture()
	r.Mode = Checkout
	r.Existing = true
	r.Ownership = UserOwned
	r.Branch = "main"
	r.UserWriteAuthorizationID = "write"
	r.UserWriteAuthorizationVersion = "1"
	p.observed = Observation{Exists: true, Path: r.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Branch: r.Branch, Head: baseCommit}
	revoked := false
	c.Validate = func(context.Context) error {
		if revoked {
			return errFixture
		}
		return nil
	}
	port := &callbackPort{fakePort: p}
	ticket, pre := Preflight(context.Background(), r, c.PreflightContext, port)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	s.hook = func(e effects.Evidence) error {
		if e.Phase == effects.IntentPhase {
			port.afterObserve = func() { revoked = true }
		}
		return nil
	}
	got := Apply(context.Background(), ticket, c, port)
	if got.Outcome != effects.Refused {
		t.Fatalf("completed after host grant revoked during final observation: %s", got.Outcome)
	}
}

func TestFinalCreatedObservationRevocationRetainsMutation(t *testing.T) {
	r, c, p, _ := fixture()
	revoked := false
	c.Validate = func(context.Context) error {
		if revoked {
			return errFixture
		}
		return nil
	}
	port := &callbackPort{fakePort: p, afterObserve: func() {
		if p.creates > 0 {
			revoked = true
		}
	}}
	out := apply(t, r, c, port)
	if out.Outcome != effects.Partial || len(out.Obligations) == 0 || !out.Evidence.Attachments[0].Created || p.creates != 1 || p.removes != 0 {
		t.Fatal(out)
	}
}
func TestFinalRemovalObservationCancellationRetainsMutation(t *testing.T) {
	r, c, p := retirementFixture(t)
	ticket, pre := CheckRetirement(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := &callbackPort{fakePort: p, afterObserve: func() {
		if p.removes > 0 {
			cancel()
		}
	}}
	out := Retire(ctx, ticket, c, port)
	if out.Outcome != effects.Partial || len(out.Obligations) == 0 || p.removes != 1 || !out.Evidence.Attachments[0].SafetyComplete {
		t.Fatal(out)
	}
}
func TestFinalSafetyRevocationRetainsBeforeRetirement(t *testing.T) {
	r, c, p := retirementFixture(t)
	revoked := false
	c.Validate = func(context.Context) error {
		if revoked {
			return errFixture
		}
		return nil
	}
	port := &callbackPort{fakePort: p, afterSafety: func() { revoked = true }}
	if _, out := CheckRetirement(context.Background(), r, c.PreflightContext, port); out.Outcome != effects.Conflict || len(out.Obligations) == 0 || p.removes != 0 {
		t.Fatal(out)
	}
}
func TestResumeObservationRevocationRetainsTrustedReceipt(t *testing.T) {
	r, c, p, _ := fixture()
	done := apply(t, r, c, p)
	revoked := false
	c.Validate = func(context.Context) error {
		if revoked {
			return errFixture
		}
		return nil
	}
	port := &callbackPort{fakePort: p, afterObserve: func() { revoked = true }}
	out := InspectResume(context.Background(), r, c.PreflightContext, port, done.Evidence)
	if out.Outcome != effects.Partial || len(out.Obligations) == 0 || !out.Evidence.Attachments[0].Created {
		t.Fatal(out)
	}
}

func TestRetirementFinalObservationRevocationRetainsRecovery(t *testing.T) {
	for _, inspect := range []bool{false, true} {
		t.Run(map[bool]string{false: "retire", true: "inspect"}[inspect], func(t *testing.T) {
			r, c, p := retirementFixture(t)
			ticket, pre := CheckRetirement(context.Background(), r, c.PreflightContext, p)
			if pre.Outcome != effects.Prepared {
				t.Fatal(pre)
			}
			var done effects.Result
			if inspect {
				done = Retire(context.Background(), ticket, c, p)
				if done.Outcome != effects.Removed {
					t.Fatal(done)
				}
			}
			revoked := false
			c.Validate = func(context.Context) error {
				if revoked {
					return errFixture
				}
				return nil
			}
			port := &callbackPort{fakePort: p, afterObserve: func() {
				if p.removes > 0 {
					revoked = true
				}
			}}
			var out effects.Result
			if inspect {
				out = InspectRetirement(context.Background(), r, c.PreflightContext, port, done.Evidence)
			} else {
				out = Retire(context.Background(), ticket, c, port)
			}
			if out.Outcome != effects.Partial || len(out.Obligations) == 0 || p.removes != 1 || !out.Evidence.Attachments[0].SafetyComplete {
				t.Fatal(out)
			}
		})
	}
}
