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
	type entry struct {
		id string
		at time.Time
	}
	var ordered []entry
	for id, set := range state.Sets {
		ordered = append(ordered, entry{id, set.Set.CapturedAt})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].at.Equal(ordered[j].at) {
			return ordered[i].id < ordered[j].id
		}
		return ordered[i].at.After(ordered[j].at)
	})
	p := a.policy.Retention
	now := time.Now()
	out.Bytes = state.StorageBytes
	for i, item := range ordered {
		set := state.Sets[item.id]
		pinned := false
		for _, pin := range state.Pins {
			if pin.SetID == item.id && pin.Completion == "" {
				pinned = true
				break
			}
		}
		if set.Pending || manifestUncertain(set.Manifest) {
			out.Uncertain++
		}
		if pinned {
			out.Pinned++
		}
		expired := p.MaxAge > 0 && !set.Set.CapturedAt.IsZero() && now.Sub(set.Set.CapturedAt) > p.MaxAge
		overCount := p.MaxSnapshotSets > 0 && i >= p.MaxSnapshotSets
		if !set.Pending && !manifestUncertain(set.Manifest) && !pinned && (expired || overCount) {
			out.Eligible = append(out.Eligible, item.id)
		} else {
			out.Keep = append(out.Keep, item.id)
		}
	}
	return out, h.check()
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
