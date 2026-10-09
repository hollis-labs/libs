package scheduler

import (
	"errors"
	"testing"
	"time"
)

func TestAnchoredIntervalsDoNotDrift(t *testing.T) {
	anchor := time.Date(2026, 11, 1, 0, 0, 0, 123, time.UTC)
	for _, s := range []Schedule{{Interval: 90 * time.Minute, Anchor: anchor}, {CronExpr: "@every 90m", Anchor: anchor}} {
		for _, c := range []struct{ from, want time.Time }{{anchor.Add(-time.Second), anchor}, {anchor, anchor.Add(90 * time.Minute)}, {anchor.Add(95 * time.Minute), anchor.Add(180 * time.Minute)}, {anchor.Add(24 * time.Hour), anchor.Add(25*time.Hour + 30*time.Minute)}} {
			got, err := NextRunForSchedule(s, c.from)
			if err != nil || !got.Equal(c.want) {
				t.Fatalf("Next(%v)=%v,%v want%v", c.from, got, err, c.want)
			}
		}
	}
}
func TestEveryAlreadyWorksInLegacyHelper(t *testing.T) {
	if err := ValidateCron("@every 5m"); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next, err := NextRun("@every 5m", from)
	if err != nil || !next.Equal(from.Add(5*time.Minute)) {
		t.Fatalf("%v %v", next, err)
	}
	// New schedule semantics preserve subsecond periods and anchor phase.
	next, err = NextRunForSchedule(Schedule{CronExpr: "@every 500ms", Anchor: from}, from.Add(250*time.Millisecond))
	if err != nil || !next.Equal(from.Add(500*time.Millisecond)) {
		t.Fatalf("%v %v", next, err)
	}
}
func TestInvalidScheduleInputs(t *testing.T) {
	for _, s := range []Schedule{{Location: "Local", CronExpr: "0 9 * * *"}, {Location: "Not/AZone", CronExpr: "0 9 * * *"}, {Location: "Europe/London", CronExpr: "CRON_TZ=America/New_York 0 9 * * *"}, {Interval: -1}, {Interval: time.Minute, CronExpr: "* * * * *"}, {CronExpr: "@every 0s"}, {CronExpr: "@every -1m"}, {KeepLastN: -1}, {Jitter: -1}, {Anchor: time.Now()}} {
		if err := ValidateSchedule(s); !errors.Is(err, ErrInvalidSchedule) {
			t.Errorf("%+v err=%v", s, err)
		}
	}
	if err := ValidateSchedule(Schedule{Location: "Europe/London", CronExpr: "CRON_TZ=Europe/London 0 9 * * *"}); err != nil {
		t.Fatal(err)
	}
}
func TestCronDSTSpringSkipsMissingHour(t *testing.T) {
	s := Schedule{CronExpr: "30 2 * * *", Location: "America/New_York"}
	from := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)
	next, err := NextRunForSchedule(s, from)
	want := time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC)
	if err != nil || !next.Equal(want) {
		t.Fatalf("next=%v err=%v want%v", next, err, want)
	}
}
func TestCronDSTFallRepeatsOnlyOnce(t *testing.T) {
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	s := Schedule{CronExpr: "30 1 * * *", Location: "America/New_York", LastRun: first}
	next, err := NextRunForSchedule(s, first.Add(5*time.Minute))
	want := time.Date(2026, 11, 2, 6, 30, 0, 0, time.UTC)
	if err != nil || !next.Equal(want) {
		t.Fatalf("next=%v err=%v want%v", next, err, want)
	}
}

type fixedRandom int64

func (f fixedRandom) Int63n(int64) int64 { return int64(f) }
func TestJitterBoundsAndInvalidSource(t *testing.T) {
	for _, n := range []int64{0, 1, 99, 100} {
		if got := DrawJitter(100, fixedRandom(n)); got != time.Duration(n) {
			t.Fatal(got)
		}
	}
	for _, n := range []int64{-1, 101} {
		if got := DrawJitter(100, fixedRandom(n)); got != 0 {
			t.Fatal(got)
		}
	}
	if DrawJitter(100, nil) != 0 || DrawJitter(0, fixedRandom(1)) != 0 {
		t.Fatal("disabled jitter")
	}
}

func TestCronDSTFallDoesNotReplayEarlierMinutes(t *testing.T) {
	last := time.Date(2026, 11, 1, 5, 59, 0, 0, time.UTC)
	s := Schedule{CronExpr: "* * * * *", Location: "America/New_York", LastRun: last}
	next, err := NextRunForSchedule(s, last)
	want := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	if err != nil || !next.Equal(want) {
		t.Fatalf("repeated minute replay: %v,%v", next, err)
	}
}
