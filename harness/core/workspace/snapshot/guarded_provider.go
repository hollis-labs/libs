package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/substrate/harness/sandbox"
)

type GuardedConfig struct {
	StorePath string
	Access    sandbox.ResolvedAccessPolicy
	Targets   TargetPlan
	Policy    *CapturePolicy
}

// GuardedProvider couples eligible scope, private mirror and native admission.
// A protected production issuer is unavailable until the host can furnish
// actually enforced live isolation/custody; policy data cannot substitute.
type GuardedProvider struct {
	guard       *storeGuard
	admission   *Admission
	git         *ShadowGit
	plan        TargetPlan
	credentials []fs.FileInfo
}

// NewGuardedProvider refuses without a supported enforced isolation producer.
// It does not create directories, a ledger, Git objects or an admission claim.
// A resolved policy and mode0700 alone cannot prove same-UID confidentiality.
func NewGuardedProvider(config GuardedConfig) (*GuardedProvider, error) {
	if err := config.Policy.Validate(); err != nil {
		return nil, err
	}
	return nil, ErrStoreCustodyUnsupported
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
	if ctx == nil || p == nil || p.guard == nil || p.admission == nil || p.git == nil || intent.TargetMapDigest != p.plan.Digest() {
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
		usage, e := prepareMirror(bounded, scope, mirror, CaptureLimits{MaxBytes: min(b.MaxRootBytes, b.MaxCaptureBytes-workBytes), MaxFileBytes: min(b.MaxRootBytes, b.MaxCaptureBytes-workBytes), MaxEntries: b.MaxCaptureEntries - workEntries}, credentials)
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
		workBytes += usage.Bytes
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
		rs, e := p.git.captureOne(bounded, Target{ID: scope.Binding.ID, Root: mirror})
		if e != nil {
			d.Code = CaptureFailed
			return result, ErrShadowStoreUnavailable
		}
		if err = p.verifyObjects(bounded, rs); err != nil {
			d.Code = CaptureFailed
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
	receipt, err := lease.finish(bounded, set, after-before, d.manifest())
	if err != nil {
		return result, err
	}
	result.retained = receipt
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
	return errors.Join(p.guard.close(), p.admission.directory.Close())
}
