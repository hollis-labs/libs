package snapshot

import (
	"sort"
	"time"
)

// CaptureCode is safe to project into host events; raw Git errors are not.
type CaptureCode string

const (
	CaptureFailed       CaptureCode = "capture_failed"
	StoreUnavailable    CaptureCode = "store_unavailable"
	CoverageUnavailable CaptureCode = "coverage_unavailable"
	CaptureDeferred     CaptureCode = "deferred"
)

type CaptureRootDescription struct {
	RootID, StoreID       string
	StartedAt, FinishedAt time.Time
	TreeHash, CommitHash  string
	Code                  CaptureCode
	Skipped               map[string]int
	references            []string
}

// CaptureDescription is data, never capture authority. Complete is relative
// to the bound effective eligible policy, not all files on the filesystem.
type CaptureDescription struct {
	SetID, OperationID, RunID, InputDigest, TargetMapDigest, PolicyRevision string
	StartedAt, FinishedAt                                                   time.Time
	Roots                                                                   []CaptureRootDescription
	Code                                                                    CaptureCode
	Complete                                                                bool
}

// CaptureResult is privately issued only after actual ingestion, verification
// and durable retained-set accounting. Decoded descriptions cannot mint it.
type CaptureResult struct {
	description CaptureDescription
	retained    *RetainedSet
}

func (r CaptureResult) Description() CaptureDescription {
	d := r.description
	d.Roots = append([]CaptureRootDescription(nil), d.Roots...)
	for i, root := range d.Roots {
		d.Roots[i].references = append([]string(nil), root.references...)
		d.Roots[i].Skipped = make(map[string]int, len(root.Skipped))
		for reason, n := range root.Skipped {
			d.Roots[i].Skipped[reason] = n
		}
	}
	return d
}

func (r CaptureResult) Retained() (*RetainedSet, error) {
	if r.retained == nil {
		return nil, ErrAdmissionUnavailable
	}
	return r.retained, nil
}

// manifest is the private durable projection, not a description supplied by a
// caller. Its binding is checked and persisted by the same capture lease.
func (d CaptureDescription) manifest() RetainedManifest {
	m := RetainedManifest{Observation: SnapshotInterval{StartedAt: d.StartedAt, FinishedAt: d.FinishedAt}, Complete: d.Complete}
	for _, r := range d.Roots {
		out := RetainedRootOutcome{RootID: r.RootID, StoreID: r.StoreID, TreeHash: r.TreeHash, CommitHash: r.CommitHash, Code: string(r.Code), Observation: SnapshotInterval{StartedAt: r.StartedAt, FinishedAt: r.FinishedAt}}
		out.References = append([]string(nil), r.references...)
		keys := make([]string, 0, len(r.Skipped))
		for reason := range r.Skipped {
			keys = append(keys, reason)
		}
		sort.Strings(keys)
		for _, reason := range keys {
			if r.Skipped[reason] > 0 {
				out.Skipped = append(out.Skipped, RetainedSkip{Reason: reason, Count: uint64(r.Skipped[reason])})
			}
		}
		m.Roots = append(m.Roots, out)
	}
	return m
}
