package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

type GCMode string

const (
	GCCollect GCMode = "collect"
	GCPurge   GCMode = "purge"
)

// GCIntent is data, never independent deletion authority. Purge is also
// constrained by KEEP and complete pins; it is not a force operation.
type GCIntent struct {
	OperationID, InputDigest, PolicyDigest, InstanceID, BindingFence string
	ControllerEpoch                                                  uint64
	Mode                                                             GCMode
}

func (i GCIntent) validate() error {
	if !safeReceiptID(i.OperationID) || !safeReceiptID(i.InstanceID) || !safeReceiptID(i.BindingFence) || i.ControllerEpoch == 0 || len(i.InputDigest) != 64 || !validObjectID(i.InputDigest) || len(i.PolicyDigest) != 64 || !validObjectID(i.PolicyDigest) || (i.Mode != GCCollect && i.Mode != GCPurge) {
		return ErrAdmissionUnavailable
	}
	return nil
}
func gcDigest(i GCIntent) string {
	b, _ := json.Marshal(i)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (p *GuardedProvider) RetentionDigest() string {
	if p == nil || p.admission == nil {
		return ""
	}
	b, _ := json.Marshal(p.admission.policy)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// GCGrant is opaque, operation-bound deletion/isolation authority held through
// reclamation. No public constructor, JSON or caller flag can issue one.
// A real production backend/current-authority issuer is not supplied here.
type GCGrant struct {
	provider  *GuardedProvider
	digest    string
	deadline  time.Time
	hostCheck func(context.Context) error
	held      *admissionLock
}

func (g *GCGrant) verify(ctx context.Context, p *GuardedProvider, i GCIntent) error {
	if ctx == nil || g == nil || g.provider != p || p == nil || p.guard == nil || p.admission == nil || g.digest != gcDigest(i) || !time.Now().Before(g.deadline) || i.PolicyDigest != p.RetentionDigest() {
		return ErrAdmissionUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.guard.check(); err != nil {
		return err
	}
	if g.hostCheck != nil {
		return g.hostCheck(ctx)
	}
	// A production provider cannot use the package-private fixture grant.
	if p.isolation != nil {
		return ErrAdmissionUnavailable
	}
	return nil
}

type gcRecord struct {
	Intent        GCIntent
	Digest, Phase string
	Sets          []string
	Result        GCResult
}

func collectionPending(s admissionLedger) bool {
	return collectionPendingExcept(s, "")
}

func collectionPendingExcept(s admissionLedger, operation string) bool {
	for id, r := range s.RetentionOperations {
		if id != operation && r.Phase != "complete" {
			return true
		}
	}
	for _, r := range s.Collections {
		if r.Phase != "complete" {
			return true
		}
	}
	return false
}

// GCResult carries redacted accounting, not a path/error/content inventory.
// Partial means effects may have occurred; the durable journal blocks later
// admission until genuine recovery establishes their disposition.
type GCResult struct {
	OperationID       string
	Decision          RetentionDecision
	RemovedSets       int
	ReclaimedBytes    int64
	Partial, Complete bool
}

// Collect never invokes legacy age/count Cleanup/Purge. It verifies the entire
// retained ref/object map, journals before mutation, CAS-deletes owned refs,
// and reclaims only unreachable private objects while the store lock and
// independently issued grant remain held. Unknown outcomes retain the journal.
func (p *GuardedProvider) Collect(ctx context.Context, i GCIntent, grant *GCGrant) (out GCResult, err error) {
	out.OperationID = i.OperationID
	if i.validate() != nil {
		return out, ErrAdmissionUnavailable
	}
	if err = grant.verify(ctx, p, i); err != nil {
		return out, err
	}
	deadline := time.Now().Add(p.admission.policy.Budgets.MaxDuration)
	if grant.deadline.Before(deadline) {
		deadline = grant.deadline
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	h := grant.held
	ownLock := h == nil
	if ownLock {
		h, err = p.admission.lock(bounded)
		if err != nil {
			return out, err
		}
	} else if err = h.check(); err != nil {
		return out, err
	}
	defer func() {
		if ownLock {
			if closeErr := h.close(); closeErr != nil {
				out.Complete = false
				out.Partial = out.Partial || out.RemovedSets > 0
				err = errors.Join(err, closeErr)
			}
		}
	}()
	state, err := p.admission.load()
	if err != nil {
		return out, err
	}
	if prior, ok := state.Collections[i.OperationID]; ok {
		if prior.Digest != gcDigest(i) || prior.Phase != "complete" {
			return out, ErrAdmissionUnavailable
		}
		if err = grant.verify(bounded, p, i); err != nil {
			return out, err
		}
		return detachedGCResult(prior.Result), h.check()
	}
	if collectionPendingExcept(state, hostGCOperation(grant, i)) {
		return out, ErrAdmissionUnavailable
	}
	decisionState := state
	if grant.hostCheck != nil {
		// The current host GC reservation is ours; every other pending outcome
		// still dominates the retention decision.
		decisionState.RetentionOperations = nil
	}
	out.Decision = p.admission.retentionDecisionLocked(decisionState, time.Now())
	// Unknown captures may have ingested objects without a retained receipt.
	// Even a no-op decision cannot account their disposition as complete.
	if out.Decision.Uncertain > 0 {
		return out, ErrAdmissionUnavailable
	}
	if i.Mode == GCPurge && len(out.Decision.Keep) != 0 {
		return out, ErrSnapshotPinned
	}
	if len(out.Decision.Eligible) == 0 {
		out.Complete = true
		if state.Collections == nil {
			state.Collections = map[string]gcRecord{}
		}
		state.Collections[i.OperationID] = gcRecord{Intent: i, Digest: gcDigest(i), Phase: "complete", Result: detachedGCResult(out)}
		if err = p.admission.save(h, state); err != nil {
			out.Complete = false
			return out, err
		}
		if err = grant.verify(bounded, p, i); err != nil {
			out.Complete = false
		}
		return out, err
	}
	targets, err := p.ownedReferences(bounded, state)
	if err != nil {
		return out, err
	}
	before, err := p.measureStore(bounded, p.admission.policy.Budgets.MaxCaptureEntries)
	if err != nil {
		return out, err
	}
	if err = grant.verify(bounded, p, i); err != nil {
		return out, err
	}
	if state.Collections == nil {
		state.Collections = map[string]gcRecord{}
	}
	record := gcRecord{Intent: i, Digest: gcDigest(i), Phase: "prepared", Sets: append([]string(nil), out.Decision.Eligible...)}
	state.Collections[i.OperationID] = record
	if err = p.admission.save(h, state); err != nil {
		return out, err
	}
	out.Partial = true
	// Deterministic root/ref ordering; every delete is exact old-object CAS.
	selected := map[string]bool{}
	for _, id := range record.Sets {
		selected[id] = true
	}
	ids := make([]string, 0, len(targets))
	for id, refs := range targets {
		for _, r := range refs {
			if selected[r.set] {
				ids = append(ids, id)
				break
			}
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		refs := targets[id]
		names := make([]string, 0, len(refs))
		for ref := range refs {
			names = append(names, ref)
		}
		sort.Strings(names)
		dir, e := p.git.openShadowRepo(id)
		if e != nil {
			return out, ErrAdmissionUnavailable
		}
		if err = grant.verify(bounded, p, i); err != nil {
			return out, err
		}
		if err = h.check(); err != nil {
			return out, err
		}
		if _, err = boundedSnapshotGit(bounded, p.git, dir, 1024, "read-tree", "--empty"); err != nil {
			return out, err
		}
		for _, ref := range names {
			r := refs[ref]
			if !selected[r.set] {
				continue
			}
			if err = grant.verify(bounded, p, i); err != nil {
				return out, err
			}
			if err = h.check(); err != nil {
				return out, err
			}
			if _, err = boundedSnapshotGit(bounded, p.git, dir, 1024, "update-ref", "-d", ref, r.oid); err != nil {
				return out, err
			}
		}
	}
	record.Phase = "refs_deleted"
	state.Collections[i.OperationID] = record
	if err = p.admission.save(h, state); err != nil {
		return out, err
	}
	for _, id := range ids {
		if err = grant.verify(bounded, p, i); err != nil {
			return out, err
		}
		if err = h.check(); err != nil {
			return out, err
		}
		dir, e := p.git.openShadowRepo(id)
		if e != nil {
			return out, ErrAdmissionUnavailable
		}
		if _, err = boundedSnapshotGit(bounded, p.git, dir, 1024, "-c", "gc.autoDetach=false", "-c", "gc.reflogExpire=now", "-c", "gc.reflogExpireUnreachable=now", "gc", "--prune=now", "--quiet"); err != nil {
			return out, err
		}
	}
	if err = grant.verify(bounded, p, i); err != nil {
		return out, err
	}
	if err = h.check(); err != nil {
		return out, err
	}
	after, err := p.measureStore(bounded, p.admission.policy.Budgets.MaxCaptureEntries)
	if err != nil {
		return out, err
	}
	for _, id := range record.Sets {
		s := state.Sets[id]
		s.Collected = true
		state.Sets[id] = s
		state.StorageBytes -= s.Bytes
	}
	out.RemovedSets = len(record.Sets)
	if before > after {
		out.ReclaimedBytes = before - after
	}
	out.Complete = true
	out.Partial = false
	record.Phase = "complete"
	record.Result = detachedGCResult(out)
	state.Collections[i.OperationID] = record
	if err = p.admission.save(h, state); err != nil {
		out.Partial = true
		out.Complete = false
		return out, err
	}
	if err = grant.verify(bounded, p, i); err != nil {
		out.Partial = true
		out.Complete = false
	}
	return out, err
}
func detachedGCResult(r GCResult) GCResult {
	r.Decision.Keep = append([]string(nil), r.Decision.Keep...)
	r.Decision.Eligible = append([]string(nil), r.Decision.Eligible...)
	return r
}

type ownedRef struct{ set, oid string }

func (p *GuardedProvider) ownedReferences(ctx context.Context, s admissionLedger) (map[string]map[string]ownedRef, error) {
	out := map[string]map[string]ownedRef{}
	for setID, set := range s.Sets {
		if set.Pending {
			return nil, ErrAdmissionUnavailable
		}
		for _, root := range set.Manifest.Roots {
			if root.Code != "" {
				return nil, ErrAdmissionUnavailable
			}
			if out[root.RootID] == nil {
				out[root.RootID] = map[string]ownedRef{}
			}
			if set.Collected {
				continue
			}
			if len(root.References) == 0 {
				return nil, ErrAdmissionUnavailable
			}
			for _, ref := range root.References {
				if _, ok := out[root.RootID][ref]; ok {
					return nil, ErrAdmissionUnavailable
				}
				out[root.RootID][ref] = ownedRef{setID, root.CommitHash}
			}
		}
	}
	for id, refs := range out {
		dir, e := p.git.openShadowRepo(id)
		if e != nil {
			return nil, ErrAdmissionUnavailable
		}
		actual, e := snapshotRefs(ctx, p.git, dir, p.admission.policy.Budgets.MaxCaptureEntries)
		if e != nil || len(actual) != len(refs) {
			return nil, ErrAdmissionUnavailable
		}
		for name, r := range refs {
			if actual[name] != r.oid {
				return nil, ErrAdmissionUnavailable
			}
		}
		if e = completeObjectOwnership(ctx, p.git, dir, p.admission.policy.Budgets.MaxCaptureEntries); e != nil {
			return nil, e
		}
	}
	return out, nil
}
