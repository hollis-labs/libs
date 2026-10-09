package scheduler

import (
	"context"
	"errors"
	"time"
)

func (e *Engine) materializeSchedule(ctx context.Context, schedule Schedule, now time.Time) {
	if !schedule.Enabled || schedule.NextRun.IsZero() {
		return
	}
	if err := ValidateSchedule(schedule); err != nil {
		e.engineError(ctx, Fire{ScheduleID: schedule.ID}, now, "validate_schedule", err)
		return
	}
	if err := ValidatePolicies(schedule); err != nil {
		e.engineError(ctx, Fire{ScheduleID: schedule.ID}, now, "validate_policy", err)
		return
	}
	grace := schedule.MisfireGrace
	if grace == 0 {
		grace = DefaultMisfireGrace
	}
	limit := schedule.MaxCatchUp
	if limit == 0 {
		limit = DefaultMaxCatchUp
	}
	if limit > e.dueBatchLimit {
		limit = e.dueBatchLimit
	}
	for count := 0; !schedule.NextRun.After(now); count++ {
		at := schedule.NextRun.UTC()
		stale := now.Sub(at) > grace
		policy := schedule.Misfire
		reason := ""
		status := FirePending
		through := time.Time{}
		base := now
		if policy == MisfireRunAll && count < limit {
			base = at
			if stale {
				reason = "misfire_run_all"
			}
		} else if policy == MisfireRunAll {
			status, reason, through = FireSkipped, "catchup_limit", now
		} else if stale {
			through = now
			if policy == MisfireSkip {
				status, reason = FireSkipped, "misfire_skip"
			} else {
				reason = "misfire_coalesced"
			}
		}
		next := now.Add(oneTimeHorizon)
		if !IsOneTime(schedule) {
			var err error
			next, err = NextRunForSchedule(schedule, base)
			if err != nil || next.IsZero() || !next.After(at) {
				if err == nil {
					err = ErrInvalidSchedule
				}
				e.engineError(ctx, Fire{ScheduleID: schedule.ID}, now, "calculate_next_run", err)
				return
			}
		}
		fire := Fire{ID: DeriveFireID(schedule.ID, at), ScheduleID: schedule.ID,
			ScheduledAt: at, Status: status, NextAttemptAt: at.Add(e.jitter(schedule.Jitter)),
			Retry: schedule.Retry, JobType: schedule.JobType, Payload: append([]byte(nil), schedule.Payload...),
			Overlap: schedule.Overlap, MaxQueuedFires: schedule.MaxQueuedFires, Reason: reason, CoalescedThrough: through}
		if status == FireSkipped {
			fire.NextAttemptAt = time.Time{}
		}
		created, err := e.store.CreateFire(ctx, FireCreation{ScheduleID: schedule.ID, ExpectedNext: at, NextRun: next.UTC(), Fire: fire})
		if errors.Is(err, ErrQueueFull) {
			fire.Status, fire.Reason, fire.NextAttemptAt = FireSkipped, "queue_full", time.Time{}
			status = FireSkipped
			created, err = e.store.CreateFire(ctx, FireCreation{ScheduleID: schedule.ID, ExpectedNext: at, NextRun: next.UTC(), Fire: fire})
		}
		if err != nil {
			e.engineError(ctx, fire, now, "create_fire", err)
			return
		}
		if !created {
			e.skip(ctx, fire, now, "materialization_conflict")
			return
		}
		if reason != "" {
			e.observe(ctx, ObserverEvent{Kind: ObserverMisfire, At: now, Fire: fire, Reason: reason, MisfireCount: 1})
		}
		if status == FireSkipped {
			e.skip(ctx, fire, now, fire.Reason)
		}
		if IsOneTime(schedule) {
			if err := e.store.DisableSchedule(ctx, schedule.ID); err != nil {
				e.engineError(ctx, fire, now, "disable_schedule", err)
				return
			}
			e.observe(ctx, ObserverEvent{Kind: ObserverDisable, At: now, Fire: fire})
			return
		}
		schedule.LastRun, schedule.NextRun = at, next
		if policy != MisfireRunAll {
			return
		}
	}
}
