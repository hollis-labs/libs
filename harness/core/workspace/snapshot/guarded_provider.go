package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/substrate/harness/sandbox"
)

type GuardedConfig struct {
	StorePath         string
	Access            sandbox.ResolvedAccessPolicy
	Targets           TargetPlan
	Policy            *CapturePolicy
	Isolation         *IsolationProof
	Host              SnapshotHost
	Redactor          ContentRedactor
	RedactionRevision string
}

// GuardedProvider couples eligible scope, private mirror and native admission.
// A protected production issuer is unavailable until the host can furnish
// actually enforced live isolation/custody; policy data cannot substitute.
type GuardedProvider struct {
	operationMu       sync.Mutex
	guard             *storeGuard
	admission         *Admission
	git               *ShadowGit
	plan              TargetPlan
	credentials       []fs.FileInfo
	isolation         *IsolationProof
	host              SnapshotHost
	redactor          ContentRedactor
	redactionRevision string
	pendingConfig     *GuardedConfig
}

// NewGuardedProvider refuses without a supported enforced isolation producer.
// It does not create directories, a ledger, Git objects or an admission claim.
// A resolved policy and mode0700 alone cannot prove same-UID confidentiality.
func NewGuardedProvider(config GuardedConfig) (*GuardedProvider, error) {
	if err := config.Policy.Validate(); err != nil {
		return nil, err
	}
	if config.Isolation == nil || config.Host == nil || config.Redactor == nil || !safeReceiptID(config.RedactionRevision) || config.Isolation.store != filepath.Clean(config.StorePath) || config.Isolation.targetDigest != config.Targets.Digest() {
		return nil, ErrStoreCustodyUnsupported
	}
	if err := config.Isolation.Verify(context.Background()); err != nil {
		return nil, err
	}
	g, err := openStoreGuard(config.StorePath, config.Access)
	if err != nil {
		return nil, err
	}
	// Construction is effectless. First capture acquires independent authority
	// before creating a ledger or Git state in this physically verified store.
	detached := config
	detached.Policy = cloneCapturePolicy(config.Policy)
	return &GuardedProvider{guard: g, plan: config.Targets, isolation: config.Isolation, host: config.Host, redactor: config.Redactor, redactionRevision: config.RedactionRevision, pendingConfig: &detached}, nil
}

func cloneCapturePolicy(policy *CapturePolicy) *CapturePolicy {
	out := *policy
	out.Retention.Roots = make(map[string]RootRetention, len(policy.Retention.Roots))
	for id, r := range policy.Retention.Roots {
		out.Retention.Roots[id] = r
	}
	return &out
}

// newOwnedGuardedProvider is a private native kernel construction boundary.
// Package-owned fixtures exercise it without a live agent. It is intentionally
// not exposed as a public capability constructor or a production fallback.
func newOwnedGuardedProvider(config GuardedConfig) (*GuardedProvider, error) {
	if config.Targets.digest == "" || len(config.Targets.roots) == 0 {
		return nil, ErrCoverageUnsupported
	}
	if err := config.Policy.Validate(); err != nil {
		return nil, err
	}
	g, err := openStoreGuard(config.StorePath, config.Access)
	if err != nil {
		return nil, err
	}
	identities, err := secretFileIdentities(config.Targets.credentialPaths)
	if err != nil {
		g.close()
		return nil, err
	}
	a, err := newAdmission(config.StorePath, config.Policy)
	if err != nil {
		g.close()
		return nil, ErrAdmissionUnavailable
	}
	sg, err := NewShadowGit(config.StorePath)
	if err != nil {
		g.close()
		a.directory.Close()
		return nil, ErrShadowStoreUnavailable
	}
	sg.guarded = true
	a.shadow = sg
	a.guard = g
	return &GuardedProvider{guard: g, admission: a, git: sg, plan: config.Targets, credentials: identities}, nil
}

// CaptureBound requires exact operation/target binding. It never derives a
// new scope from caller []Target or turns an empty selection into whole-root.
func (p *GuardedProvider) CaptureBound(ctx context.Context, intent CaptureIntent) (result CaptureResult, err error) {
	if ctx == nil || p == nil || p.guard == nil || intent.TargetMapDigest != p.plan.Digest() || intent.validate() != nil {
		return result, ErrAdmissionUnavailable
	}
	if !p.operationMu.TryLock() {
		return result, ErrAdmissionUnavailable
	}
	defer p.operationMu.Unlock()
	if p.pendingConfig != nil && intent.PolicyRevision != p.pendingConfig.Policy.Revision {
		return result, ErrAdmissionUnavailable
	}
	var hostLease SnapshotAdmission
	var op SnapshotOperation
	if p.isolation != nil {
		op = p.operation("capture", intent, intent.InputDigest)
		hostLease, err = p.acquireHost(ctx, op)
		if err != nil {
			return result, err
		}
		defer func() {
			if closeErr := hostLease.Close(); closeErr != nil {
				err = errors.Join(err, ErrAdmissionUnavailable)
				result.description.Complete = false
				result.description.Code = StoreUnavailable
			}
		}()
		if p.pendingConfig != nil {
			if e := hostLease.Record(ctx, op, SnapshotEffect{Phase: "store_initialize_intent", Outcome: "pending"}); e != nil {
				return result, ErrAdmissionUnavailable
			}
			if e := p.verifyHost(ctx, hostLease, op); e != nil {
				return result, e
			}
			kernel, e := newOwnedGuardedProvider(*p.pendingConfig)
			if e != nil {
				return result, e
			}
			p.guard.close()
			p.guard, p.admission, p.git, p.credentials = kernel.guard, kernel.admission, kernel.git, kernel.credentials
			p.admission.isolation, p.admission.host, p.admission.redactionRevision = p.isolation, p.host, p.redactionRevision
			p.pendingConfig = nil
			if e := p.verifyHost(ctx, hostLease, op); e != nil {
				return result, e
			}
			if e := hostLease.Record(ctx, op, SnapshotEffect{Phase: "store_initialized", Outcome: "pending"}); e != nil {
				return result, ErrAdmissionUnavailable
			}
		}
	}
	if p.admission == nil || p.git == nil {
		return result, ErrAdmissionUnavailable
	}
	d := CaptureDescription{SetID: intent.SetID, OperationID: intent.OperationID, RunID: intent.RunID, InputDigest: intent.InputDigest, TargetMapDigest: intent.TargetMapDigest, PolicyRevision: p.admission.policy.Revision, StartedAt: time.Now().UTC()}
	defer func() {
		if d.FinishedAt.IsZero() {
			d.FinishedAt = time.Now().UTC()
		}
		if err != nil {
			d.Complete = false
			if d.Code == "" {
				d.Code = CaptureFailed
			}
		}
		result.description = d
	}()
	if err = p.guard.check(); err != nil {
		d.Code = StoreUnavailable
		return result, ErrStoreCustodyUnsupported
	}
	lease, err := p.admission.beginCapture(ctx, intent)
	if err != nil {
		d.Code = CaptureDeferred
		return result, err
	}
	defer func() {
		if e := lease.retainUncertain(); e != nil {
			err = errors.Join(err, ErrAdmissionUnavailable)
			d.Complete = false
			d.Code = StoreUnavailable
		}
	}()
	verify := func() error {
		if e := lease.check(ctx); e != nil {
			return e
		}
		if hostLease != nil {
			return p.verifyHost(ctx, hostLease, op)
		}
		return p.guard.check()
	}
	if err = verify(); err != nil {
		return result, err
	}
	if hostLease != nil {
		if err = hostLease.Record(ctx, op, SnapshotEffect{Phase: "capture_intent", Outcome: "pending"}); err != nil {
			return result, ErrAdmissionUnavailable
		}
	}
	// Host-named credential sources can appear or be replaced between captures.
	// Refresh metadata under admission before reading candidate bytes; never
	// substitute a caller-selected credential list for the immutable plan.
	credentials, e := secretFileIdentities(p.plan.credentialPaths)
	if e != nil {
		d.Code = CoverageUnavailable
		return result, e
	}
	b := lease.Budgets()
	bounded, cancel := context.WithTimeout(ctx, b.MaxDuration)
	defer cancel()
	if len(p.plan.roots) > b.MaxRoots {
		d.Code = CaptureDeferred
		return result, ErrSnapshotBudget
	}
	before, err := p.measureStore(bounded, b.MaxCaptureEntries)
	if err != nil {
		d.Code = StoreUnavailable
		return result, err
	}
	set := SnapshotSet{ID: intent.SetID, CapturedAt: time.Now().UTC(), Roots: map[string]RootSnapshot{}}
	var workBytes int64
	var workEntries int
	for _, scope := range p.plan.roots {
		rootResult := CaptureRootDescription{RootID: scope.Binding.ID, StoreID: lease.RootStoreID(scope.Binding.ID), StartedAt: time.Now().UTC(), Skipped: map[string]int{}}
		if err = lease.check(bounded); err != nil {
			d.Code = CaptureDeferred
			return result, err
		}
		if err = p.guard.check(); err != nil {
			d.Code = StoreUnavailable
			return result, err
		}
		stageID, e := randomAdmissionID()
		if e != nil {
			return result, ErrAdmissionUnavailable
		}
		stage := "mirror-" + stageID
		if e = p.guard.root.Mkdir(stage, 0700); e != nil {
			return result, ErrAdmissionUnavailable
		}
		mirror := filepath.Join(p.guard.path, stage)
		if err = verify(); err != nil {
			return result, err
		}
		usage, e := prepareMirrorRedacted(bounded, scope, mirror, CaptureLimits{MaxBytes: min(b.MaxRootBytes, b.MaxCaptureBytes-workBytes), MaxFileBytes: min(b.MaxRootBytes, b.MaxCaptureBytes-workBytes), MaxEntries: b.MaxCaptureEntries - workEntries}, credentials, p.redactor)
		if e != nil {
			rootResult.Code = CoverageUnavailable
			rootResult.FinishedAt = time.Now().UTC()
			d.Roots = append(d.Roots, rootResult)
			d.Code = CoverageUnavailable
			// Confined staging is retained on uncertain identity; do not remove
			// through a newly substituted named store after custody loss.
			if p.guard.check() == nil {
				_ = p.guard.root.RemoveAll(stage)
			}
			return result, e
		}
		workBytes += max(usage.Bytes, usage.InputBytes)
		workEntries += usage.Entries
		// Reserve conservative Git/mirror overhead before object ingestion.
		if workBytes > b.MaxCaptureBytes/2 || int64(workEntries) > (b.MaxCaptureBytes-workBytes*2)/8192 {
			d.Code = CaptureDeferred
			return result, ErrSnapshotBudget
		}
		if err = lease.check(bounded); err != nil {
			return result, err
		}
		if err = p.guard.check(); err != nil {
			return result, err
		}
		refsBefore := map[string]string{}
		if err = verify(); err != nil {
			return result, err
		}
		if dir, e := p.git.openShadowRepo(scope.Binding.ID); e == nil {
			refsBefore, e = snapshotRefs(bounded, p.git, dir, b.MaxCaptureEntries)
			if e != nil {
				return result, e
			}
		} else if _, e := p.guard.root.Stat(filepath.Join("targets", filepath.Base(p.git.shadowKeyDir(scope.Binding.ID)), "shadow.git")); !errors.Is(e, fs.ErrNotExist) {
			return result, ErrAdmissionUnavailable
		}
		rs, e := p.git.captureOne(bounded, Target{ID: scope.Binding.ID, Root: mirror})
		if e != nil {
			d.Code = CaptureFailed
			return result, ErrShadowStoreUnavailable
		}
		if err = p.verifyObjects(bounded, rs); err != nil {
			d.Code = CaptureFailed
			return result, err
		}
		rootResult.references, err = captureReferences(bounded, p.git, scope.Binding.ID, rs.CommitHash, refsBefore, b.MaxCaptureEntries)
		if err != nil {
			return result, err
		}
		if err = p.guard.check(); err != nil {
			return result, err
		}
		if err = p.guard.root.RemoveAll(stage); err != nil {
			return result, ErrAdmissionUnavailable
		}
		rs.Root = scope.Binding.Root
		set.Roots[scope.Binding.ID] = rs
		rootResult.TreeHash = rs.TreeHash
		rootResult.CommitHash = rs.CommitHash
		rootResult.FinishedAt = time.Now().UTC()
		if usage.Excluded > 0 {
			rootResult.Skipped["excluded"] = usage.Excluded
		}
		for _, skip := range rs.Skipped {
			rootResult.Skipped[string(skip.Reason)]++
		}
		d.Roots = append(d.Roots, rootResult)
	}
	if err = lease.check(bounded); err != nil {
		return result, err
	}
	if err = p.guard.check(); err != nil {
		return result, err
	}
	after, err := p.measureStore(bounded, b.MaxCaptureEntries)
	if err != nil {
		return result, err
	}
	if after > b.MaxStorageBytes || after < before || after-before > b.MaxCaptureBytes {
		return result, ErrSnapshotBudget
	}
	d.Complete = true
	for _, r := range d.Roots {
		if r.Skipped["oversize_untracked"] > 0 {
			d.Complete = false
		}
	}
	d.FinishedAt = time.Now().UTC()
	if err = verify(); err != nil {
		return result, err
	}
	receipt, err := lease.finish(bounded, set, after-before, d.manifest())
	if err != nil {
		return result, err
	}
	result.retained = receipt
	if hostLease != nil {
		if err = p.verifyHost(ctx, hostLease, op); err != nil {
			return result, err
		}
		if err = hostLease.Record(ctx, op, SnapshotEffect{Phase: "capture_retained", Outcome: "complete"}); err != nil {
			return result, ErrAdmissionUnavailable
		}
	}
	return result, nil
}

func (p *GuardedProvider) verifyObjects(ctx context.Context, r RootSnapshot) error {
	gitDir := p.git.gitDirFor(r.TargetID)
	for _, object := range []string{r.TreeHash + "^{tree}", r.CommitHash + "^{commit}"} {
		if _, err := p.git.runGit(ctx, gitDir, "", nil, "cat-file", "-e", object); err != nil {
			return ErrShadowStoreUnavailable
		}
	}
	tree, err := p.git.runGit(ctx, gitDir, "", nil, "rev-parse", r.CommitHash+"^{tree}")
	if err != nil || strings.TrimSpace(string(tree)) != r.TreeHash {
		return ErrShadowStoreUnavailable
	}
	return nil
}

func (p *GuardedProvider) measureStore(ctx context.Context, maxEntries int) (int64, error) {
	var total int64
	entries := 0
	err := fs.WalkDir(p.guard.root.FS(), ".", func(_ string, e fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return ErrStoreCustodyUnsupported
		}
		entries++
		if entries > maxEntries {
			return ErrSnapshotBudget
		}
		info, err := e.Info()
		if err != nil {
			return ErrStoreCustodyUnsupported
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return ErrStoreCustodyUnsupported
		}
		if info.Size() < 0 || total > p.admission.policy.Budgets.MaxStorageBytes-info.Size() {
			return ErrSnapshotBudget
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// Close releases native descriptors; it never removes retained evidence.
func (p *GuardedProvider) Close() error {
	if p == nil {
		return nil
	}
	if !p.operationMu.TryLock() {
		return ErrAdmissionUnavailable
	}
	defer p.operationMu.Unlock()
	if p.admission == nil {
		return p.guard.close()
	}
	return errors.Join(p.guard.close(), p.admission.directory.Close())
}
