package sqlstore

import (
	"time"

	"github.com/hollis-labs/libs/util/scheduler"
)

type scheduleOptions struct {
	Location       string
	Interval       time.Duration
	Anchor         time.Time
	Jitter         time.Duration
	KeepLastN      int
	Overlap        scheduler.OverlapPolicy
	Misfire        scheduler.MisfirePolicy
	MisfireGrace   time.Duration
	MaxCatchUp     int
	MaxQueuedFires int
}

func scheduleOptionsFrom(s scheduler.Schedule) scheduleOptions {
	return scheduleOptions{s.Location, s.Interval, s.Anchor, s.Jitter, s.KeepLastN,
		s.Overlap, s.Misfire, s.MisfireGrace, s.MaxCatchUp, s.MaxQueuedFires}
}

func (o scheduleOptions) apply(s *scheduler.Schedule) {
	s.Location, s.Interval, s.Anchor, s.Jitter, s.KeepLastN = o.Location, o.Interval, o.Anchor, o.Jitter, o.KeepLastN
	s.Overlap, s.Misfire, s.MisfireGrace, s.MaxCatchUp, s.MaxQueuedFires = o.Overlap, o.Misfire, o.MisfireGrace, o.MaxCatchUp, o.MaxQueuedFires
}

type fireOptions struct {
	Overlap          scheduler.OverlapPolicy
	MaxQueuedFires   int
	Reason           string
	CoalescedThrough time.Time
}

func fireOptionsFrom(f scheduler.Fire) fireOptions {
	return fireOptions{f.Overlap, f.MaxQueuedFires, f.Reason, f.CoalescedThrough}
}

func (o fireOptions) apply(f *scheduler.Fire) {
	f.Overlap, f.MaxQueuedFires, f.Reason, f.CoalescedThrough = o.Overlap, o.MaxQueuedFires, o.Reason, o.CoalescedThrough
}
