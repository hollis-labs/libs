package conformance

import (
	"testing"
	"time"

	"github.com/hollis-labs/libs/util/scheduler"
)

func testPruneRetention(t *testing.T, f *fixture) {
	sch := f.schedule(t, func(s *scheduler.Schedule) { s.KeepLastN = 1 })
	var first scheduler.Fire
	for i := range 3 {
		c := f.creation(sch)
		c.Fire.ScheduledAt = sch.NextRun
		c.Fire.ID = scheduler.DeriveFireID(sch.ID, sch.NextRun)
		c.Fire.NextAttemptAt = sch.NextRun
		c.NextRun = sch.NextRun.Add(time.Minute)
		if created, err := f.store.CreateFire(ctx(), c); err != nil || !created {
			t.Fatalf("create=%v,%v", created, err)
		}
		claimed := f.claim(t, c.Fire, time.Duration(i)*time.Minute)
		if won, err := f.store.TransitionFire(ctx(), transition(claimed, scheduler.FireSucceeded)); err != nil || !won {
			t.Fatalf("terminal=%v,%v", won, err)
		}
		if i == 0 {
			first = c.Fire
		}
		sch.NextRun = c.NextRun
	}
	// Pending/claimed/retrying obligations are never pruned, regardless of age.
	pending := f.fire(t)
	claimed := f.claim(t, f.fire(t), 0)
	retry := f.claim(t, f.fire(t), 0)
	tr := transition(retry, scheduler.FireRetrying)
	tr.NextAttemptAt = base
	if won, err := f.store.TransitionFire(ctx(), tr); err != nil || !won {
		t.Fatalf("retry=%v,%v", won, err)
	}
	n, err := f.store.Prune(ctx(), base.Add(time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("Prune=%d,%v want2", n, err)
	}
	for _, id := range []string{pending.ID, claimed.ID, retry.ID} {
		if !f.isDue(t, id, 2*time.Hour) {
			t.Errorf("obligation %s pruned", id)
		}
	}
	// Resetting an occurrence request against the current schedule CAS must not
	// recreate a pruned ID (the fence, rather than current NextRun, refuses it).
	c := f.creation(sch)
	c.Fire = first
	c.Fire.Status = scheduler.FirePending
	c.ExpectedNext = sch.NextRun
	c.NextRun = sch.NextRun.Add(time.Minute)
	if won, err := f.store.CreateFire(ctx(), c); err != nil || won {
		t.Fatalf("pruned recreate=%v,%v", won, err)
	}
	if n, err := f.store.Prune(ctx(), base.Add(2*time.Hour)); err != nil || n != 0 {
		t.Fatalf("keep-last-N/idempotent=%d,%v", n, err)
	}
}
