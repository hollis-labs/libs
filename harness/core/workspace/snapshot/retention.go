package snapshot

import (
	"context"
	"errors"
	"sort"
	"time"
)

// OwnerCompletion is issued by a snapshot owner adapter only after that
// operation's durable completion. Decoding data or a bare acknowledgment does
// not issue one. Until an adapter can establish this fact its references KEEP.
type OwnerCompletion struct {
	admission                      *Admission
	setID, owner, operationID, pin string
}

// ownerCompletion is intentionally private. Fork/capture/event owner adapters
// must call it only after recording the corresponding full operation outcome.
func (l *ReadLease) ownerCompletion(ctx context.Context, operationID string) (*OwnerCompletion, error) {
	if operationID == "" {
		return nil, ErrAdmissionUnavailable
	}
	if err := l.Verify(ctx); err != nil {
		return nil, err
	}
	p := l.pin
	state, err := l.admission.load()
	if err != nil {
		return nil, err
	}
	return &OwnerCompletion{admission: l.admission, setID: l.set.Intent.SetID, owner: state.Pins[p].Owner, operationID: operationID, pin: p}, nil
}

// RetentionDecision is observational. It is not deletion permission and does
// not release references. Pending/unknown accounting is always retained.
type RetentionDecision struct {
	Keep, Eligible    []string
	Pinned, Uncertain int
	Bytes             int64
}

// retentionDecision requires the complete store lock and ledger. It operates
// on multi-root sets, not independent target age/count trims.
func (a *Admission) retentionDecision(ctx context.Context) (RetentionDecision, error) {
	var out RetentionDecision
	h, err := a.lock(ctx)
	if err != nil {
		return out, err
	}
	defer h.close()
	state, err := a.load()
	if err != nil {
		return out, err
	}
	if state.StoreID != a.storeID {
		return out, ErrAdmissionUnavailable
	}
	return a.retentionDecisionLocked(state, time.Now()), h.check()
}

// retentionDecisionLocked requires the complete store ledger under its stable
// lock. Root overrides cannot split a set or override any outstanding pin.
func (a *Admission) retentionDecisionLocked(state admissionLedger, now time.Time) RetentionDecision {
	out := RetentionDecision{Bytes: state.StorageBytes}
	ids := make([]string, 0, len(state.Sets))
	for id, s := range state.Sets {
		if !s.Collected {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		x, y := state.Sets[ids[i]].Set.CapturedAt, state.Sets[ids[j]].Set.CapturedAt
		if x.Equal(y) {
			return ids[i] < ids[j]
		}
		return x.After(y)
	})
	ranks := map[string]int{}
	for global, id := range ids {
		s := state.Sets[id]
		pinned := false
		for _, p := range state.Pins {
			if p.SetID == id && p.Completion == "" {
				pinned = true
				break
			}
		}
		uncertain := s.Pending || manifestUncertain(s.Manifest) || collectionPending(state)
		if uncertain {
			out.Uncertain++
		}
		if pinned {
			out.Pinned++
		}
		eligible := len(s.Set.Roots) > 0
		for root := range s.Set.Roots {
			age, count, rank := a.policy.Retention.MaxAge, a.policy.Retention.MaxSnapshotSets, global
			if override, ok := a.policy.Retention.Roots[root]; ok {
				age, count, rank = override.MaxAge, override.MaxSnapshotSets, ranks[root]
			}
			expired := age > 0 && !s.Set.CapturedAt.IsZero() && now.Sub(s.Set.CapturedAt) > age
			excess := count > 0 && rank >= count
			if !expired && !excess {
				eligible = false
			}
			ranks[root]++
		}
		if eligible && !pinned && !uncertain {
			out.Eligible = append(out.Eligible, id)
		} else {
			out.Keep = append(out.Keep, id)
		}
	}
	return out
}

// Cleanup and Purge are deliberately not routed to the legacy unguarded
// operations. A safe GC implementation also needs complete physical-object,
// noncooperating-reader and current deletion authority; the ledger alone does
// not supply those capabilities. This admission never manufactures them.
func (a *Admission) Cleanup(ctx context.Context) (RetentionDecision, error) {
	if a == nil {
		return RetentionDecision{}, ErrAdmissionUnavailable
	}
	d, err := a.retentionDecision(ctx)
	if err != nil {
		return d, err
	}
	if len(d.Eligible) > 0 {
		return d, ErrAdmissionUnavailable
	}
	return d, nil
}
func (a *Admission) Purge(ctx context.Context) error {
	if a == nil {
		return ErrAdmissionUnavailable
	}
	d, err := a.retentionDecision(ctx)
	if err != nil {
		return err
	}
	if d.Pinned > 0 || d.Uncertain > 0 {
		return ErrSnapshotPinned
	}
	return ErrAdmissionUnavailable
}

func completeReadLease(ctx context.Context, l *ReadLease, c *OwnerCompletion) error {
	if c == nil || c.admission != l.admission || c.setID != l.set.Intent.SetID || c.pin != l.pin || c.operationID == "" {
		return ErrAdmissionUnavailable
	}
	if err := l.Verify(ctx); err != nil {
		return err
	}
	state, err := l.admission.load()
	if err != nil {
		return err
	}
	p := state.Pins[l.pin]
	if p.Owner != c.owner {
		return ErrAdmissionUnavailable
	}
	p.Completion = c.operationID
	state.Pins[l.pin] = p
	if err = l.admission.save(l.held, state); err != nil {
		return err
	}
	return errors.Join(l.Close(), ctx.Err())
}
