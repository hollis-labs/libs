package conformance

import (
	"testing"
	"time"

	"github.com/hollis-labs/libs/util/scheduler"
)

func testTimingRoundTrip(t *testing.T, f *fixture) {
	want := scheduler.Schedule{ID: "timing", Location: "America/New_York", Interval: 90 * time.Minute, Anchor: base, Jitter: time.Second, KeepLastN: 3, NextRun: base, Enabled: true, JobType: "j", Retry: scheduler.RetryPolicy{Backoff: scheduler.BackoffPolicy{Jitter: time.Second}}}
	if err := f.seed.CreateSchedule(ctx(), want); err != nil {
		t.Fatal(err)
	}
	due, err := f.store.ListDueSchedules(ctx(), base, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due %v,%v", due, err)
	}
	got := due[0]
	if got.Location != want.Location || got.Interval != want.Interval || !got.Anchor.Equal(want.Anchor) || got.Jitter != want.Jitter || got.KeepLastN != want.KeepLastN || got.Retry != want.Retry {
		t.Fatalf("lost timing settings: %+v", got)
	}
	bad := want
	bad.ID = "invalid-zone"
	bad.Location = "Not/AZone"
	if err = f.seed.CreateSchedule(ctx(), bad); err == nil {
		t.Fatal("accepted invalid zone")
	}
	due, err = f.store.ListDueSchedules(ctx(), base, 10)
	if err != nil || len(due) != 1 {
		t.Fatal("invalid schedule was materialized")
	}
}
