package scheduler

import (
	"errors"
	"fmt"
	"time"
)

// OverlapPolicy controls attempts whose schedule already has an active claim.
type OverlapPolicy string

const (
	// OverlapSkip terminally skips another occurrence while a claim is active.
	OverlapSkip OverlapPolicy = "skip"
	// OverlapQueue leaves occurrences pending, bounded by MaxQueuedFires.
	OverlapQueue OverlapPolicy = "queue"
	// OverlapAllow permits simultaneous attempts of different occurrences.
	OverlapAllow OverlapPolicy = "allow"
)

// MisfirePolicy controls occurrences observed after their grace window.
type MisfirePolicy string

const (
	// MisfireSkip records stale occurrences without dispatching them.
	MisfireSkip MisfirePolicy = "skip"
	// MisfireRunOnce coalesces missed occurrences into one dispatch (the default).
	MisfireRunOnce MisfirePolicy = "run_once"
	// MisfireRunAll dispatches missed occurrences up to MaxCatchUp per tick.
	MisfireRunAll MisfirePolicy = "run_all"
)

const (
	// DefaultMisfireGrace tolerates ordinary polling delays.
	DefaultMisfireGrace = time.Minute
	// DefaultMaxCatchUp bounds materialization work for a stale schedule.
	DefaultMaxCatchUp = 100
	// DefaultMaxQueuedFires bounds outstanding queued occurrences per schedule.
	DefaultMaxQueuedFires = 100
)

// ErrScheduleBusy means another unexpired claim holds this schedule. Stores
// must determine this atomically with ClaimFire, across engine instances.
var ErrScheduleBusy = errors.New("scheduler: schedule has an active claim")

// ErrQueueFull refuses pending materialization above the durable queue bound.
// The engine may materialize the same occurrence as an explicit skipped fire.
var ErrQueueFull = errors.New("scheduler: schedule queue is full")

func overlapPolicy(p OverlapPolicy) OverlapPolicy {
	if p == "" {
		return OverlapSkip
	}
	return p
}

// ValidatePolicies rejects unknown policies and negative policy bounds.
func ValidatePolicies(s Schedule) error {
	switch overlapPolicy(s.Overlap) {
	case OverlapSkip, OverlapQueue, OverlapAllow:
	default:
		return fmt.Errorf("scheduler: invalid overlap policy %q", s.Overlap)
	}
	switch s.Misfire {
	case "", MisfireSkip, MisfireRunOnce, MisfireRunAll:
	default:
		return fmt.Errorf("scheduler: invalid misfire policy %q", s.Misfire)
	}
	if s.MisfireGrace < 0 || s.MaxCatchUp < 0 || s.MaxQueuedFires < 0 {
		return errors.New("scheduler: policy bounds must not be negative")
	}
	return nil
}
