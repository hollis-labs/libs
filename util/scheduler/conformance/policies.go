package conformance

import (
	"errors"
	"github.com/hollis-labs/libs/util/scheduler"
	"testing"
	"time"
)

func testQueueFullExplicitDecision(t *testing.T, f *fixture) {
	sch := f.schedule(t, func(s *scheduler.Schedule) { s.Overlap = scheduler.OverlapQueue; s.MaxQueuedFires = 1 })
	c := f.creation(sch)
	c.Fire.Overlap = scheduler.OverlapQueue
	c.Fire.MaxQueuedFires = 1
	if ok, err := f.store.CreateFire(ctx(), c); err != nil || !ok {
		t.Fatalf("first %v,%v", ok, err)
	}
	sch.NextRun = c.NextRun
	excess := f.creation(sch)
	excess.Fire.Overlap = scheduler.OverlapQueue
	excess.Fire.MaxQueuedFires = 1
	if ok, err := f.store.CreateFire(ctx(), excess); ok || !errors.Is(err, scheduler.ErrQueueFull) {
		t.Fatalf("full queue must refuse without advancing %v,%v", ok, err)
	}
	// Retrying the same CAS as an explicit terminal decision proves the refusal
	// left both the occurrence and schedule untouched.
	excess.Fire.Status = scheduler.FireSkipped
	excess.Fire.Reason = "queue_full"
	excess.Fire.NextAttemptAt = time.Time{}
	if ok, err := f.store.CreateFire(ctx(), excess); err != nil || !ok {
		t.Fatalf("explicit skipped decision %v,%v", ok, err)
	}
	if f.isDue(t, excess.Fire.ID, time.Hour) {
		t.Fatal("queue-full terminal occurrence became dispatchable")
	}
}

func testQueueClaimExclusion(t *testing.T, f *fixture) {
	sch := f.schedule(t, func(s *scheduler.Schedule) { s.Overlap = scheduler.OverlapQueue; s.MaxQueuedFires = 2 })
	var fires []scheduler.Fire
	for range 2 {
		c := f.creation(sch)
		c.NextRun = sch.NextRun.Add(10 * time.Second)
		c.Fire.Overlap = scheduler.OverlapQueue
		c.Fire.MaxQueuedFires = 2
		if ok, err := f.store.CreateFire(ctx(), c); err != nil || !ok {
			t.Fatalf("create %v,%v", ok, err)
		}
		fires = append(fires, c.Fire)
		sch.NextRun = c.NextRun
	}
	claimed := f.claim(t, fires[0], 0)
	due, err := f.store.ListDueFires(ctx(), base.Add(30*time.Second), 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("queued sibling should not consume due batch: %+v,%v", due, err)
	}
	_, won, err := f.store.ClaimFire(ctx(), scheduler.FireClaim{FireID: fires[1].ID, ExpectedStatus: scheduler.FirePending, ClaimedAt: base.Add(30 * time.Second), ClaimExpiresAt: base.Add(2 * time.Minute)})
	if won || !errors.Is(err, scheduler.ErrScheduleBusy) {
		t.Fatalf("direct claim bypassed sibling exclusion %v,%v", won, err)
	}
	if ok, transitionErr := f.store.TransitionFire(ctx(), transition(claimed, scheduler.FireSucceeded)); transitionErr != nil || !ok {
		t.Fatalf("release %v,%v", ok, transitionErr)
	}
	if !f.isDue(t, fires[1].ID, time.Hour) {
		t.Fatal("queued sibling never became eligible after release")
	}
}
