package workspace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace/snapshot"
)

type unavailableGCHost struct {
	calls, records int
	reason         SnapshotGCReason
}

func (h *unavailableGCHost) AcquireSnapshotGC(_ context.Context, r SnapshotGCReason) (*snapshot.GuardedProvider, snapshot.GCIntent, *snapshot.GCGrant, error) {
	h.calls++
	h.reason = r
	return nil, snapshot.GCIntent{}, nil, nil
}
func (h *unavailableGCHost) RecordSnapshotGC(context.Context, snapshot.GCResult) error {
	h.records++
	return nil
}
func TestSnapshotGCOnDemandDoesNotMintAuthority(t *testing.T) {
	h := &unavailableGCHost{}
	r, err := CollectSnapshots(context.Background(), h)
	if !errors.Is(err, snapshot.ErrAdmissionUnavailable) || r.Complete || h.calls != 1 || h.records != 1 || h.reason != SnapshotGCOnDemand {
		t.Fatal("missing producer became cleanup success")
	}
}
func TestSnapshotGCScheduleFiniteAndStopsOnUnavailable(t *testing.T) {
	h := &unavailableGCHost{}
	if err := RunSnapshotGC(context.Background(), SnapshotGCSchedule{}, h); !errors.Is(err, snapshot.ErrAdmissionUnavailable) || h.calls != 0 {
		t.Fatal("zero schedule enabled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunSnapshotGC(ctx, SnapshotGCSchedule{Interval: time.Hour, AttemptTimeout: time.Second, MaxAttempts: 1}, h); !errors.Is(err, context.Canceled) || h.calls != 0 {
		t.Fatal("canceled schedule acquired")
	}
	if err := RunSnapshotGC(context.Background(), SnapshotGCSchedule{Interval: time.Millisecond, AttemptTimeout: time.Second, MaxAttempts: 2}, h); !errors.Is(err, snapshot.ErrAdmissionUnavailable) || h.calls != 1 || h.reason != SnapshotGCScheduled {
		t.Fatal("unavailable collector retried")
	}
}
