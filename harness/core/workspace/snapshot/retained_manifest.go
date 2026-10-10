package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"time"
	"unicode"
)

// RetainedManifest binds sanitized outcomes to the same durable receipt as the
// retained successful objects. Failed roots are explicit and never acquire
// invented tree/commit hashes. An all-failed attempt has no retained receipt.
type RetainedManifest struct {
	Observation SnapshotInterval
	Roots       []RetainedRootOutcome
	Complete    bool
}
type SnapshotInterval struct{ StartedAt, FinishedAt time.Time }
type RetainedRootOutcome struct {
	RootID, StoreID, TreeHash, CommitHash, Code string
	Observation                                 SnapshotInterval
	Skipped                                     []RetainedSkip
	// References are exact capture-owned Git refs. They are private retention
	// evidence, never event payload or deletion authority. Absence keeps objects.
	References []string
}
type RetainedSkip struct {
	Reason string
	Count  uint64
}

func (i SnapshotInterval) valid() bool {
	return !i.StartedAt.IsZero() && !i.FinishedAt.IsZero() && !i.FinishedAt.Before(i.StartedAt)
}
func safeReceiptID(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
func rootStoreID(storeID, target string) string {
	s := sha256.Sum256([]byte("snapshot.root-store.v1\x00" + storeID + "\x00" + target))
	return hex.EncodeToString(s[:])
}
func (l *CaptureLease) RootStoreID(target string) string {
	if l == nil || l.admission == nil {
		return ""
	}
	return rootStoreID(l.admission.storeID, target)
}
func detachedManifest(m RetainedManifest) RetainedManifest {
	out := m
	out.Roots = append([]RetainedRootOutcome(nil), m.Roots...)
	for i := range out.Roots {
		out.Roots[i].Skipped = append([]RetainedSkip(nil), m.Roots[i].Skipped...)
		out.Roots[i].References = append([]string(nil), m.Roots[i].References...)
	}
	return out
}
func (m RetainedManifest) validate(set SnapshotSet, storeID string) error {
	if !m.Observation.valid() || len(m.Roots) == 0 || len(m.Roots) > 128 || set.CapturedAt.Before(m.Observation.StartedAt) || set.CapturedAt.After(m.Observation.FinishedAt) {
		return ErrAdmissionUnavailable
	}
	seen := map[string]bool{}
	successful := 0
	for _, r := range m.Roots {
		if !safeReceiptID(r.RootID) || seen[r.RootID] || r.StoreID != rootStoreID(storeID, r.RootID) || !r.Observation.valid() || r.Observation.StartedAt.Before(m.Observation.StartedAt) || r.Observation.FinishedAt.After(m.Observation.FinishedAt) || len(r.Skipped) > 16 {
			return ErrAdmissionUnavailable
		}
		seen[r.RootID] = true
		refs := map[string]bool{}
		if len(r.References) > 1024 || (r.Code != "" && len(r.References) != 0) {
			return ErrAdmissionUnavailable
		}
		for _, ref := range r.References {
			if !validRetainedReference(ref) || refs[ref] {
				return ErrAdmissionUnavailable
			}
			refs[ref] = true
		}
		source, ok := set.Roots[r.RootID]
		switch r.Code {
		case "":
			if !ok || source.Err != nil || source.TargetID != r.RootID || source.TreeHash != r.TreeHash || source.CommitHash != r.CommitHash || !validObjectID(r.TreeHash) || !validObjectID(r.CommitHash) {
				return ErrAdmissionUnavailable
			}
			successful++
		case "capture_failed", "store_unavailable", "coverage_unavailable", "deferred":
			if ok || m.Complete || r.TreeHash != "" || r.CommitHash != "" {
				return ErrAdmissionUnavailable
			}
		default:
			return ErrAdmissionUnavailable
		}
		skipped := map[string]bool{}
		for _, s := range r.Skipped {
			if s.Count == 0 || skipped[s.Reason] {
				return ErrAdmissionUnavailable
			}
			skipped[s.Reason] = true
			switch s.Reason {
			case "excluded", "git_metadata":
			case "oversize_untracked", "unsupported", "unobserved":
				if m.Complete {
					return ErrAdmissionUnavailable
				}
			default:
				return ErrAdmissionUnavailable
			}
		}
	}
	if successful == 0 || successful != len(set.Roots) {
		return ErrAdmissionUnavailable
	}
	return nil
}
func validRetainedReference(ref string) bool {
	if !strings.HasPrefix(ref, "refs/snapshots/") || len(ref) > 512 || strings.HasSuffix(ref, ".") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.ContainsAny(ref, " ~^:?*[\\\x00") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
		for _, r := range part {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}
func accountDigest(storeID string, s setAccount) (string, error) {
	encoded, err := json.Marshal(struct {
		StoreID        string
		Intent         CaptureIntent
		PolicyRevision string
		Set            SnapshotSet
		Manifest       RetainedManifest
		Bytes          int64
	}{storeID, s.Intent, s.PolicyRevision, s.Set, s.Manifest, s.Bytes})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("snapshot.retained.v1\x00"), encoded...))
	return hex.EncodeToString(sum[:]), nil
}
func (l *ReadLease) Manifest() RetainedManifest {
	if l == nil {
		return RetainedManifest{}
	}
	return detachedManifest(l.set.Manifest)
}
func (a *Admission) validateLedger(s admissionLedger) error {
	var total int64
	runs := map[string]runAccount{}
	for id, set := range s.Sets {
		if id != set.Intent.SetID || set.Intent.validate() != nil || set.PolicyRevision != set.Intent.PolicyRevision || set.Bytes < 0 || set.Bytes > int64(^uint64(0)>>1)-total {
			return ErrAdmissionUnavailable
		}
		if !set.Collected {
			total += set.Bytes
		}
		if set.Collected && set.Pending {
			return ErrAdmissionUnavailable
		}
		run := runs[set.Intent.RunID]
		if set.Bytes > int64(^uint64(0)>>1)-run.Bytes {
			return ErrAdmissionUnavailable
		}
		run.Captures++
		run.Bytes += set.Bytes
		runs[set.Intent.RunID] = run
		if !set.Pending {
			if set.Set.ID != id || set.Manifest.validate(set.Set, s.StoreID) != nil {
				return ErrAdmissionUnavailable
			}
			digest, e := accountDigest(s.StoreID, set)
			if e != nil || digest != set.Digest {
				return ErrAdmissionUnavailable
			}
		}
	}
	if total != s.StorageBytes {
		return ErrAdmissionUnavailable
	}
	for key, p := range s.Pins {
		set, ok := s.Sets[p.SetID]
		if !ok || set.Pending || !p.Kind.valid() || !safeReceiptID(p.Owner) || key != pinKey(p.SetID, p.Owner, p.Kind) {
			return ErrAdmissionUnavailable
		}
		if set.Collected && p.Completion == "" {
			return ErrAdmissionUnavailable
		}
	}
	if len(runs) != len(s.Runs) {
		return ErrAdmissionUnavailable
	}
	for id, run := range s.Runs {
		if !safeReceiptID(id) || run != runs[id] {
			return ErrAdmissionUnavailable
		}
	}
	collected := map[string]bool{}
	for id, r := range s.Collections {
		if !safeReceiptID(id) || r.Intent.OperationID != id || r.Intent.validate() != nil || r.Digest != gcDigest(r.Intent) || (r.Phase != "prepared" && r.Phase != "refs_deleted" && r.Phase != "complete") {
			return ErrAdmissionUnavailable
		}
		seen := map[string]bool{}
		for _, setID := range r.Sets {
			set, ok := s.Sets[setID]
			if !ok || seen[setID] || set.Pending || (r.Phase == "complete") != set.Collected {
				return ErrAdmissionUnavailable
			}
			seen[setID] = true
			if r.Phase == "complete" {
				if collected[setID] {
					return ErrAdmissionUnavailable
				}
				collected[setID] = true
			}
		}
		if r.Phase == "complete" {
			if !r.Result.Complete || r.Result.Partial || r.Result.OperationID != id || r.Result.RemovedSets != len(r.Sets) || r.Result.ReclaimedBytes < 0 {
				return ErrAdmissionUnavailable
			}
		} else if r.Result.Complete || r.Result.RemovedSets != 0 {
			return ErrAdmissionUnavailable
		}
	}
	for id, set := range s.Sets {
		if set.Collected != collected[id] {
			return ErrAdmissionUnavailable
		}
	}
	return nil
}
func manifestUncertain(m RetainedManifest) bool {
	for _, r := range m.Roots {
		if r.Code != "" {
			return true
		}
	}
	return false
}
