package sqlstore_test

import (
	"context"
	"database/sql"
	"github.com/hollis-labs/libs/util/scheduler"
	"github.com/hollis-labs/libs/util/scheduler/sqlstore"
	"path/filepath"
	"testing"
	"time"
)

func TestTimingOptionsRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	want := scheduler.Schedule{ID: "timed", Location: "America/New_York", Interval: 90 * time.Minute, Anchor: at, Jitter: time.Second, KeepLastN: 2, NextRun: at, Enabled: true, JobType: "j", Retry: scheduler.RetryPolicy{Backoff: scheduler.BackoffPolicy{Jitter: time.Second}}}
	if err := s.CreateSchedule(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetSchedule(ctx, want.ID)
	if err != nil || !ok || got.Location != want.Location || got.Interval != want.Interval || !got.Anchor.Equal(want.Anchor) || got.Jitter != want.Jitter || got.KeepLastN != want.KeepLastN || got.Retry != want.Retry {
		t.Fatalf("roundtrip %+v,%v,%v", got, ok, err)
	}
	for _, bad := range []scheduler.Schedule{{ID: "zone", Location: "Not/A_Zone"}, {ID: "conflict", Location: "UTC", CronExpr: "CRON_TZ=America/New_York 0 0 * * *"}} {
		if err := s.CreateSchedule(ctx, bad); err == nil {
			t.Fatalf("accepted invalid %+v", bad)
		}
		if _, ok, err := s.GetSchedule(ctx, bad.ID); err != nil || ok {
			t.Fatalf("invalid persisted %v,%v", ok, err)
		}
	}
}

func TestPruneFenceSurvivesReopenAndScheduleReplacement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retention.db")
	open := func() (*sql.DB, *sqlstore.Store) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if err = sqlstore.Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
		s, err := sqlstore.New(db)
		if err != nil {
			t.Fatal(err)
		}
		return db, s
	}
	db, s := open()
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	sch := scheduler.Schedule{ID: "s", NextRun: at, KeepLastN: 1, Enabled: true, JobType: "j"}
	if err := s.CreateSchedule(ctx, sch); err != nil {
		t.Fatal(err)
	}
	var first scheduler.Fire
	for i := range 3 {
		f := scheduler.Fire{ID: scheduler.DeriveFireID(sch.ID, sch.NextRun), ScheduleID: sch.ID, ScheduledAt: sch.NextRun, NextAttemptAt: sch.NextRun, Status: scheduler.FirePending, JobType: "j"}
		c := scheduler.FireCreation{ScheduleID: sch.ID, ExpectedNext: sch.NextRun, NextRun: sch.NextRun.Add(time.Minute), Fire: f}
		if ok, createErr := s.CreateFire(ctx, c); createErr != nil || !ok {
			t.Fatalf("create %v,%v", ok, createErr)
		}
		claimed, ok, err := s.ClaimFire(ctx, scheduler.FireClaim{FireID: f.ID, ExpectedStatus: scheduler.FirePending, ClaimedAt: f.ScheduledAt, ClaimExpiresAt: f.ScheduledAt.Add(time.Minute)})
		if err != nil || !ok {
			t.Fatalf("claim %v,%v", ok, err)
		}
		if ok, err := s.TransitionFire(ctx, scheduler.FireTransition{FireID: f.ID, From: scheduler.FireClaimed, Attempt: claimed.Attempt, ClaimedAt: claimed.FiredAt, To: scheduler.FireSucceeded}); err != nil || !ok {
			t.Fatalf("finish %v,%v", ok, err)
		}
		if i == 0 {
			first = f
		}
		sch.NextRun = c.NextRun
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Prune(canceled, at.Add(time.Hour)); err == nil {
		t.Fatal("canceled prune succeeded")
	}
	if _, ok, err := s.GetFire(ctx, first.ID); err != nil || !ok {
		t.Fatal("canceled prune removed history")
	}
	if n, err := s.Prune(ctx, at.Add(time.Hour)); err != nil || n != 2 {
		t.Fatalf("prune %d,%v", n, err)
	}
	var options int
	if err := db.QueryRow(`SELECT count(*) FROM gosched_fire_options`).Scan(&options); err != nil || options != 1 {
		t.Fatalf("orphan options %d,%v", options, err)
	}
	if err := s.DeleteSchedule(ctx, sch.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, s = open()
	defer func() { _ = db.Close() }()
	if err := s.CreateSchedule(ctx, sch); err != nil {
		t.Fatal(err)
	}
	c := scheduler.FireCreation{ScheduleID: sch.ID, ExpectedNext: sch.NextRun, NextRun: sch.NextRun.Add(time.Minute), Fire: first}
	if ok, err := s.CreateFire(ctx, c); err != nil || ok {
		t.Fatalf("recreated pruned occurrence %v,%v", ok, err)
	}
	if _, ok, err := s.GetFire(ctx, first.ID); err != nil || ok {
		t.Fatalf("pruned ID survived %v,%v", ok, err)
	}
}

func TestPruneRollsBackHistoryOptionsAndFenceOnDeleteFailure(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	if err := sqlstore.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s, err := sqlstore.New(db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	sch := scheduler.Schedule{ID: "atomic", NextRun: at, Enabled: true, JobType: "j"}
	if err = s.CreateSchedule(ctx, sch); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 3 {
		f := scheduler.Fire{ID: scheduler.DeriveFireID(sch.ID, sch.NextRun), ScheduleID: sch.ID, ScheduledAt: sch.NextRun, Status: scheduler.FireSkipped, JobType: "j"}
		c := scheduler.FireCreation{ScheduleID: sch.ID, ExpectedNext: sch.NextRun, NextRun: sch.NextRun.Add(time.Minute), Fire: f}
		if ok, createErr := s.CreateFire(ctx, c); createErr != nil || !ok {
			t.Fatalf("create %v,%v", ok, createErr)
		}
		ids = append(ids, f.ID)
		sch.NextRun = c.NextRun
	}
	// Candidates are newest first: abort the middle delete after the first fire,
	// its options and a fence update have already been written in the transaction.
	if _, err = db.Exec(`CREATE TABLE prune_fault(fire_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO prune_fault(fire_id) VALUES(?)`, ids[1]); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TRIGGER reject_prune BEFORE DELETE ON gosched_fires WHEN EXISTS(SELECT 1 FROM prune_fault WHERE fire_id=OLD.id) BEGIN SELECT RAISE(ABORT,'forced deletion failure'); END`); err != nil {
		t.Fatal(err)
	}
	if n, pruneErr := s.Prune(ctx, at.Add(time.Hour)); pruneErr == nil || n != 0 {
		t.Fatalf("partial prune %d,%v", n, pruneErr)
	}
	for _, id := range ids {
		if _, ok, readErr := s.GetFire(ctx, id); readErr != nil || !ok {
			t.Fatalf("lost history %s %v,%v", id, ok, readErr)
		}
	}
	for query, want := range map[string]int{"SELECT count(*) FROM gosched_fire_options": 3, "SELECT count(*) FROM gosched_pruned": 0} {
		var n int
		if err = db.QueryRow(query).Scan(&n); err != nil || n != want {
			t.Fatalf("partial %s state %d,%v", query, n, err)
		}
	}
	if _, err = db.Exec(`DROP TRIGGER reject_prune`); err != nil {
		t.Fatal(err)
	}
	if n, pruneErr := s.Prune(ctx, at.Add(time.Hour)); pruneErr != nil || n != 3 {
		t.Fatalf("repaired prune %d,%v", n, pruneErr)
	}
}
