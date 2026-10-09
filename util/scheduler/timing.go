package scheduler

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	_ "time/tzdata" // make named zones available without host zoneinfo

	"github.com/robfig/cron/v3"
)

// ErrInvalidSchedule identifies invalid recurrence, zone or retention inputs.
var ErrInvalidSchedule = errors.New("invalid schedule")

// RandomSource supplies bounded randomness. Engine implementations must serialize
// calls when sharing a source that is not safe for concurrent use.
type RandomSource interface{ Int63n(int64) int64 }

// DrawJitter returns an inclusive nonnegative delay bounded by max. A nil source
// disables jitter. Invalid source results also produce zero, never an early fire.
func DrawJitter(limit time.Duration, source RandomSource) time.Duration {
	if limit <= 0 || source == nil {
		return 0
	}
	bound := int64(limit)
	if bound < math.MaxInt64 {
		bound++
	}
	n := source.Int63n(bound)
	if n < 0 || n > int64(limit) {
		return 0
	}
	return time.Duration(n)
}

func intervalPeriod(s Schedule) (time.Duration, error) {
	expr := strings.TrimSpace(s.CronExpr)
	if s.Interval < 0 {
		return 0, fmt.Errorf("%w: interval must be positive", ErrInvalidSchedule)
	}
	if s.Interval > 0 {
		if expr != "" {
			return 0, fmt.Errorf("%w: interval and cron expression are mutually exclusive", ErrInvalidSchedule)
		}
		return s.Interval, nil
	}
	if strings.HasPrefix(expr, "@every ") {
		period, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(expr, "@every ")))
		if err != nil || period <= 0 {
			return 0, fmt.Errorf("%w: invalid @every duration", ErrInvalidSchedule)
		}
		return period, nil
	}
	return 0, nil
}

// ValidateSchedule rejects invalid settings before a store accepts a schedule.
// Location and CRON_TZ/TZ may agree; conflicting zones are refused.
func ValidateSchedule(s Schedule) error {
	if s.KeepLastN < 0 || s.Jitter < 0 {
		return fmt.Errorf("%w: negative retention or jitter", ErrInvalidSchedule)
	}
	if err := s.Retry.Validate(); err != nil {
		return err
	}
	period, err := intervalPeriod(s)
	if err != nil {
		return err
	}
	if s.Location != "" {
		if s.Location == "Local" {
			return fmt.Errorf("%w: Location must name a portable zone, not Local", ErrInvalidSchedule)
		}
		if _, locationErr := time.LoadLocation(s.Location); locationErr != nil {
			return fmt.Errorf("%w: location %q: %w", ErrInvalidSchedule, s.Location, locationErr)
		}
	}
	if period > 0 {
		return nil
	}
	if !s.Anchor.IsZero() {
		return fmt.Errorf("%w: anchor requires interval", ErrInvalidSchedule)
	}
	if strings.TrimSpace(s.CronExpr) == "" {
		return nil
	}
	_, err = cronForSchedule(s)
	return err
}

// IsOneTime distinguishes empty legacy cron schedules from fixed intervals.
func IsOneTime(s Schedule) bool { return s.Interval == 0 && strings.TrimSpace(s.CronExpr) == "" }

func cronForSchedule(s Schedule) (cron.Schedule, error) {
	expr := strings.TrimSpace(s.CronExpr)
	if s.Location != "" {
		if strings.HasPrefix(expr, "CRON_TZ=") || strings.HasPrefix(expr, "TZ=") {
			parts := strings.SplitN(expr, " ", 2)
			if len(parts) != 2 || strings.SplitN(parts[0], "=", 2)[1] != s.Location {
				return nil, fmt.Errorf("%w: Location conflicts with expression zone", ErrInvalidSchedule)
			}
			expr = parts[1]
		}
		expr = "CRON_TZ=" + s.Location + " " + expr
	}
	parsed, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, fmt.Errorf("%w: cron: %w", ErrInvalidSchedule, err)
	}
	return parsed, nil
}

// NextRunForSchedule returns the next nominal occurrence strictly after from.
// Intervals (including @every) use Anchor, or Unix epoch when omitted. Jitter
// delays dispatch only; it must not change this phase or a fire's stable ID.
func NextRunForSchedule(s Schedule, from time.Time) (time.Time, error) {
	if err := ValidateSchedule(s); err != nil {
		return time.Time{}, err
	}
	period, _ := intervalPeriod(s)
	if period > 0 {
		anchor := s.Anchor.UTC()
		if s.Anchor.IsZero() {
			anchor = time.Unix(0, 0).UTC()
		}
		if from.Before(anchor) {
			return anchor, nil
		}
		elapsed := from.Sub(anchor)
		if elapsed == time.Duration(math.MaxInt64) {
			return time.Time{}, fmt.Errorf("%w: anchor distance exceeds duration range", ErrInvalidSchedule)
		}
		return from.UTC().Add(period - elapsed%period), nil
	}
	if IsOneTime(s) {
		return time.Time{}, nil
	}
	parsed, err := cronForSchedule(s)
	if err != nil {
		return time.Time{}, err
	}
	next := parsed.Next(from)
	// A wall-clock cron occurrence in a repeated DST hour is one occurrence.
	// LastRun carries the prior committed occurrence across process restarts.
	for sameRepeatedLocalTime(from, next, s) || sameRepeatedLocalTime(s.LastRun, next, s) {
		next = parsed.Next(next)
	}
	return next.UTC(), nil
}

func sameRepeatedLocalTime(a, b time.Time, s Schedule) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	var loc *time.Location
	if s.Location != "" {
		loc, _ = time.LoadLocation(s.Location)
	} else {
		expr := strings.TrimSpace(s.CronExpr)
		if strings.HasPrefix(expr, "CRON_TZ=") || strings.HasPrefix(expr, "TZ=") {
			loc, _ = time.LoadLocation(strings.SplitN(strings.Fields(expr)[0], "=", 2)[1])
		}
	}
	if loc == nil {
		loc = time.UTC
	}
	a, b = a.In(loc), b.In(loc)
	_, ao := a.Zone()
	_, bo := b.Zone()
	aClock := a.Hour()*3600 + a.Minute()*60 + a.Second()
	bClock := b.Hour()*3600 + b.Minute()*60 + b.Second()
	return bo < ao && a.Year() == b.Year() && a.YearDay() == b.YearDay() && bClock <= aClock
}
