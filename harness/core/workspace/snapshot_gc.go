package workspace

import (
	"context"
	"errors"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace/snapshot"
)

type SnapshotGCReason string

const (
	SnapshotGCOnDemand  SnapshotGCReason = "on_demand"
	SnapshotGCScheduled SnapshotGCReason = "scheduled"
)

// SnapshotGCHost owns scheduling and current authority. Acquire must supply an
// actually issued opaque grant, not a boolean/decoded policy or completion.
// This library supplies no production grant/isolation/owner-completion issuer.
// Record stores redacted outcomes, including partial/unsupported dispositions.
type SnapshotGCHost interface {
	AcquireSnapshotGC(context.Context, SnapshotGCReason) (*snapshot.GuardedProvider, snapshot.GCIntent, *snapshot.GCGrant, error)
	RecordSnapshotGC(context.Context, snapshot.GCResult) error
}

func CollectSnapshots(ctx context.Context, host SnapshotGCHost) (snapshot.GCResult, error) {
	return collectSnapshots(ctx, host, SnapshotGCOnDemand)
}
func collectSnapshots(ctx context.Context, host SnapshotGCHost, reason SnapshotGCReason) (out snapshot.GCResult, err error) {
	if ctx == nil || host == nil {
		return out, snapshot.ErrAdmissionUnavailable
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	provider, intent, grant, err := host.AcquireSnapshotGC(ctx, reason)
	if err != nil {
		return out, err
	}
	out, err = provider.Collect(ctx, intent, grant)
	recordErr := host.RecordSnapshotGC(ctx, out)
	if recordErr != nil {
		out.Partial = out.Partial || out.RemovedSets > 0
		out.Complete = false
	}
	return out, errors.Join(err, recordErr)
}

// SnapshotGCSchedule is explicit finite host policy. Zero never starts an
// unbounded loop; no goroutine, runtime callback or shim policy is installed.
type SnapshotGCSchedule struct {
	Interval, AttemptTimeout time.Duration
	MaxAttempts              int
}

func RunSnapshotGC(ctx context.Context, policy SnapshotGCSchedule, host SnapshotGCHost) error {
	if ctx == nil || host == nil || policy.Interval <= 0 || policy.AttemptTimeout <= 0 || policy.MaxAttempts <= 0 {
		return snapshot.ErrAdmissionUnavailable
	}
	timer := time.NewTimer(policy.Interval)
	defer timer.Stop()
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		bounded, cancel := context.WithTimeout(ctx, policy.AttemptTimeout)
		_, err := collectSnapshots(bounded, host, SnapshotGCScheduled)
		cancel()
		// Unknown/partial authority never becomes an automatic effect retry.
		if err != nil {
			return err
		}
		if attempt+1 < policy.MaxAttempts {
			timer.Reset(policy.Interval)
		}
	}
	return nil
}
