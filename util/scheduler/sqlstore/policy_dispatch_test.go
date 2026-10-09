package sqlstore_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/util/scheduler"
	"github.com/hollis-labs/libs/util/scheduler/sqlstore"
)

func plainStores(t *testing.T) (*sqlstore.Store, *sqlstore.Store) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "shared.db")
	var stores []*sqlstore.Store
	for range 2 {
		db, err := sql.Open("sqlite", file)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(4)
		t.Cleanup(func() { _ = db.Close() })
		if migrateErr := sqlstore.Migrate(context.Background(), db); migrateErr != nil {
			t.Fatal(migrateErr)
		}
		s, err := sqlstore.New(db)
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, s)
	}
	return stores[0], stores[1]
}

func TestDefaultSQLiteTwoPoolsExactlyOnce(t *testing.T) {
	a, b := plainStores(t)
	ctx := context.Background()
	for _, s := range []*sqlstore.Store{a, b} {
		var mode string
		if err := s.DB().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	const n = 40
	for i := range n {
		if err := a.CreateSchedule(ctx, scheduler.Schedule{ID: fmt.Sprint(i), Enabled: true, NextRun: now}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[string]int{}
	runner := runnerFunc(func(_ context.Context, job scheduler.Job) error {
		mu.Lock()
		seen[job.FireID]++
		mu.Unlock()
		return nil
	})
	var wg sync.WaitGroup
	for _, s := range []*sqlstore.Store{a, b} {
		engine := scheduler.New(s, runner, scheduler.WithClock(fixedClock{now}))
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := engine.TickNow(ctx); err != nil {
				t.Error(err)
			}
			if engine.Status().WorkerErrors != 0 {
				t.Errorf("errors: %+v", engine.Status())
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("fires %d, want %d", len(seen), n)
	}
	for id, count := range seen {
		f, ok, err := a.GetFire(ctx, id)
		if count != 1 || err != nil || !ok || f.Status != scheduler.FireSucceeded || f.Attempt != 1 {
			t.Fatalf("%s count=%d fire=%+v err=%v", id, count, f, err)
		}
	}
}

func TestOverlapPoliciesUseDurableSiblingClaims(t *testing.T) {
	for _, policy := range []scheduler.OverlapPolicy{"", scheduler.OverlapSkip, scheduler.OverlapQueue, scheduler.OverlapAllow} {
		t.Run(string(policy), func(t *testing.T) {
			a, b := plainStores(t)
			ctx := context.Background()
			now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
			s := scheduler.Schedule{ID: "recurring", CronExpr: "* * * * *", Enabled: true, NextRun: now, Overlap: policy}
			if err := a.CreateSchedule(ctx, s); err != nil {
				t.Fatal(err)
			}
			old := scheduler.Fire{ID: scheduler.DeriveFireID(s.ID, now), ScheduleID: s.ID, ScheduledAt: now, NextAttemptAt: now, Status: scheduler.FirePending, Overlap: policy}
			if won, err := a.CreateFire(ctx, scheduler.FireCreation{ScheduleID: s.ID, ExpectedNext: now, NextRun: now.Add(time.Minute), Fire: old}); err != nil || !won {
				t.Fatalf("create %v %v", won, err)
			}
			claim, won, err := a.ClaimFire(ctx, scheduler.FireClaim{FireID: old.ID, ExpectedStatus: scheduler.FirePending, ClaimedAt: now, ClaimExpiresAt: now.Add(time.Hour)})
			if err != nil || !won {
				t.Fatalf("claim %v %v", won, err)
			}
			calls := 0
			e := scheduler.New(b, runnerFunc(func(context.Context, scheduler.Job) error { calls++; return nil }), scheduler.WithClock(fixedClock{now.Add(time.Minute)}))
			if tickErr := e.TickNow(ctx); tickErr != nil {
				t.Fatal(tickErr)
			}
			newID := scheduler.DeriveFireID(s.ID, now.Add(time.Minute))
			f, ok, err := b.GetFire(ctx, newID)
			if !ok || err != nil {
				t.Fatalf("newfire %v %v", ok, err)
			}
			switch policy {
			case scheduler.OverlapAllow:
				if calls != 1 || f.Status != scheduler.FireSucceeded {
					t.Fatalf("allow %+v calls%d", f, calls)
				}
			case scheduler.OverlapQueue:
				if calls != 0 || f.Status != scheduler.FirePending || f.Attempt != 0 {
					t.Fatalf("queue %+v calls%d", f, calls)
				}
			default:
				if calls != 0 || f.Status != scheduler.FireSkipped || f.Reason != "overlap_skip" || f.Attempt != 0 {
					t.Fatalf("skip %+v calls%d", f, calls)
				}
			}
			if won, err := a.TransitionFire(ctx, scheduler.FireTransition{FireID: old.ID, From: scheduler.FireClaimed, Attempt: claim.Attempt, ClaimedAt: claim.FiredAt, To: scheduler.FireSucceeded}); err != nil || !won {
				t.Fatalf("release %v %v", won, err)
			}
			if policy == scheduler.OverlapQueue {
				if err := e.TickNow(ctx); err != nil {
					t.Fatal(err)
				}
				f, _, _ = b.GetFire(ctx, newID)
				if calls != 1 || f.Status != scheduler.FireSucceeded {
					t.Fatalf("queue did not drain %+v", f)
				}
			}
		})
	}
}

func TestHungDispatchDoesNotBlockLaterScheduleAndTimeoutPersists(t *testing.T) {
	s, _ := plainStores(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if err := s.CreateSchedule(ctx, scheduler.Schedule{ID: "hung", Enabled: true, NextRun: now, Retry: scheduler.RetryPolicy{MaxAttempts: 1}}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	fast := make(chan struct{})
	e := scheduler.New(s, runnerFunc(func(ctx context.Context, j scheduler.Job) error {
		if j.ScheduleID == "hung" {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		close(fast)
		return nil
	}), scheduler.WithClock(fixedClock{now}), scheduler.WithConcurrency(2), scheduler.WithFireTimeout(500*time.Millisecond))
	done := make(chan error, 1)
	go func() { done <- e.TickNow(ctx) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("no hung call")
	}
	if err := s.CreateSchedule(ctx, scheduler.Schedule{ID: "later", Enabled: true, NextRun: now}); err != nil {
		t.Fatal(err)
	}
	if err := e.TickNow(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fast:
	default:
		t.Fatal("later schedule blocked")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f, ok, err := s.GetFire(ctx, scheduler.DeriveFireID("hung", now))
	if err != nil || !ok || f.Status != scheduler.FireExhausted || f.LastError != context.DeadlineExceeded.Error() {
		t.Fatalf("timeout %+v %v", f, err)
	}
	if e.Status().WorkerErrors != 1 {
		t.Fatalf("status %+v", e.Status())
	}
	e.Stop()
}

func TestMisfirePoliciesPersistBoundedHistory(t *testing.T) {
	for _, policy := range []scheduler.MisfirePolicy{"", scheduler.MisfireSkip, scheduler.MisfireRunOnce, scheduler.MisfireRunAll} {
		t.Run(string(policy), func(t *testing.T) {
			s, _ := plainStores(t)
			ctx := context.Background()
			first := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
			now := first.Add(5 * time.Minute)
			if err := s.CreateSchedule(ctx, scheduler.Schedule{ID: "missed", Enabled: true, NextRun: first, CronExpr: "* * * * *", Misfire: policy, MaxCatchUp: 2, Overlap: scheduler.OverlapAllow}); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var events []scheduler.ObserverEvent
			calls := 0
			e := scheduler.New(s, runnerFunc(func(context.Context, scheduler.Job) error { mu.Lock(); calls++; mu.Unlock(); return nil }), scheduler.WithClock(fixedClock{now}), scheduler.WithObserver(scheduler.ObserverFunc(func(_ context.Context, event scheduler.ObserverEvent) error {
				mu.Lock()
				events = append(events, event)
				mu.Unlock()
				return nil
			})))
			if tickErr := e.TickNow(ctx); tickErr != nil {
				t.Fatal(tickErr)
			}
			want := 1
			if policy == scheduler.MisfireSkip {
				want = 0
			}
			if policy == scheduler.MisfireRunAll {
				want = 2
			}
			if calls != want {
				t.Fatalf("calls%d want%d", calls, want)
			}
			f, ok, err := s.GetFire(ctx, scheduler.DeriveFireID("missed", first))
			if err != nil || !ok {
				t.Fatalf("history %v %v", ok, err)
			}
			if policy == scheduler.MisfireSkip && (f.Status != scheduler.FireSkipped || f.Reason != "misfire_skip") {
				t.Fatalf("skip %+v", f)
			}
			if policy != scheduler.MisfireRunAll && !f.CoalescedThrough.Equal(now) {
				t.Fatalf("lost accounting span %+v", f)
			}
			if policy == scheduler.MisfireRunAll {
				f, ok, err = s.GetFire(ctx, scheduler.DeriveFireID("missed", first.Add(2*time.Minute)))
				if err != nil || !ok || f.Status != scheduler.FireSkipped || f.Reason != "catchup_limit" || !f.CoalescedThrough.Equal(now) {
					t.Fatalf("bound %+v %v", f, err)
				}
			}
			misfires := 0
			for _, event := range events {
				if event.Kind == scheduler.ObserverMisfire {
					misfires++
				}
			}
			if misfires == 0 {
				t.Fatal("no durable misfire event")
			}
			got, _, _ := s.GetSchedule(ctx, "missed")
			if !got.NextRun.Equal(now.Add(time.Minute)) {
				t.Fatalf("next %+v", got)
			}
		})
	}
}

func TestQueueBoundAndBlockedQueueDoNotStarveOtherSchedules(t *testing.T) {
	s, _ := plainStores(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	sch := scheduler.Schedule{ID: "queue", Enabled: true, NextRun: now, Interval: time.Minute, Overlap: scheduler.OverlapQueue, MaxQueuedFires: 2}
	if err := s.CreateSchedule(ctx, sch); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		at := now.Add(time.Duration(i) * time.Minute)
		f := scheduler.Fire{ID: scheduler.DeriveFireID(sch.ID, at), ScheduleID: sch.ID, ScheduledAt: at, Status: scheduler.FirePending, NextAttemptAt: at, Overlap: sch.Overlap, MaxQueuedFires: 2}
		creation := scheduler.FireCreation{ScheduleID: sch.ID, ExpectedNext: at, NextRun: at.Add(time.Minute), Fire: f}
		ok, err := s.CreateFire(ctx, creation)
		if i >= 2 {
			if !errors.Is(err, scheduler.ErrQueueFull) || ok {
				t.Fatalf("queue bound %v %v", ok, err)
			}
			creation.Fire.Status, creation.Fire.Reason, creation.Fire.NextAttemptAt = scheduler.FireSkipped, "queue_full", time.Time{}
			ok, err = s.CreateFire(ctx, creation)
		}
		if err != nil || !ok {
			t.Fatalf("create %v %v", ok, err)
		}
	}
	for i := range 4 {
		f, _, err := s.GetFire(ctx, scheduler.DeriveFireID(sch.ID, now.Add(time.Duration(i)*time.Minute)))
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && f.Status != scheduler.FirePending {
			t.Fatalf("queued %+v", f)
		}
		if i >= 2 && (f.Status != scheduler.FireSkipped || f.Reason != "queue_full") {
			t.Fatalf("overflow %+v", f)
		}
	}
	if _, ok, err := s.ClaimFire(ctx, scheduler.FireClaim{FireID: scheduler.DeriveFireID(sch.ID, now), ExpectedStatus: scheduler.FirePending, ClaimedAt: now, ClaimExpiresAt: now.Add(time.Hour)}); err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	other := scheduler.Schedule{ID: "other", Enabled: true, NextRun: now}
	if err := s.CreateSchedule(ctx, other); err != nil {
		t.Fatal(err)
	}
	f := scheduler.Fire{ID: scheduler.DeriveFireID("other", now), ScheduleID: "other", ScheduledAt: now, Status: scheduler.FirePending, NextAttemptAt: now}
	if ok, err := s.CreateFire(ctx, scheduler.FireCreation{ScheduleID: "other", ExpectedNext: now, NextRun: now.Add(time.Hour), Fire: f}); err != nil || !ok {
		t.Fatalf("other %v %v", ok, err)
	}
	due, err := s.ListDueFires(ctx, now.Add(5*time.Minute), 1)
	if err != nil || len(due) != 1 || due[0].ScheduleID != "other" {
		t.Fatalf("starvation %+v %v", due, err)
	}
}

func TestBusyTimeoutOverrideAndInvalidPolicies(t *testing.T) {
	s, _ := plainStores(t)
	if _, err := sqlstore.New(s.DB(), sqlstore.WithBusyTimeout(-time.Second)); err == nil {
		t.Fatal("negative timeout accepted")
	}
	if _, err := sqlstore.New(s.DB(), sqlstore.WithBusyTimeout(time.Duration(1<<63-1))); err == nil {
		t.Fatal("SQLite timeout overflow accepted")
	}
	custom, err := sqlstore.New(s.DB(), sqlstore.WithBusyTimeout(123*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	if err = custom.CreateSchedule(ctx, scheduler.Schedule{ID: "valid", Enabled: true, NextRun: now}); err != nil {
		t.Fatal(err)
	}
	var ms int
	if err = custom.DB().QueryRow("PRAGMA busy_timeout").Scan(&ms); err != nil || ms != 123 {
		t.Fatalf("timeout %d %v", ms, err)
	}
	for _, sch := range []scheduler.Schedule{{ID: "bad", Overlap: "unknown"}, {ID: "bad", Misfire: "unknown"}, {ID: "bad", MisfireGrace: -time.Second}, {ID: "bad", MaxCatchUp: -1}} {
		if err = s.CreateSchedule(ctx, sch); err == nil {
			t.Fatalf("accepted %+v", sch)
		}
	}
	_, ok, err := s.GetSchedule(ctx, "bad")
	if err != nil || ok {
		t.Fatalf("invalid persisted %v %v", ok, err)
	}
}

func TestEngineQueueOverflowHasDurableSkipObservation(t *testing.T) {
	s, _ := plainStores(t)
	ctx := context.Background()
	first := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	now := first.Add(5 * time.Minute)
	if err := s.CreateSchedule(ctx, scheduler.Schedule{ID: "bounded", Enabled: true, NextRun: first, Interval: time.Minute, Overlap: scheduler.OverlapQueue, MaxQueuedFires: 2, Misfire: scheduler.MisfireRunAll, MaxCatchUp: 10}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	skips := 0
	e := scheduler.New(s, runnerFunc(func(context.Context, scheduler.Job) error { return nil }), scheduler.WithClock(fixedClock{now}), scheduler.WithObserver(scheduler.ObserverFunc(func(_ context.Context, event scheduler.ObserverEvent) error {
		if event.Kind == scheduler.ObserverSkip && event.Reason == "queue_full" {
			mu.Lock()
			skips++
			mu.Unlock()
			if event.Fire.Status != scheduler.FireSkipped {
				t.Errorf("not durable %+v", event)
			}
		}
		return nil
	})))
	if err := e.TickNow(ctx); err != nil {
		t.Fatal(err)
	}
	if skips != 4 {
		t.Fatalf("overflow observations%d want4", skips)
	}
	for i := 2; i <= 5; i++ {
		f, ok, err := s.GetFire(ctx, scheduler.DeriveFireID("bounded", first.Add(time.Duration(i)*time.Minute)))
		if err != nil || !ok || f.Status != scheduler.FireSkipped || f.Reason != "queue_full" {
			t.Fatalf("overflow %+v %v", f, err)
		}
	}
}

func TestFailureObservationMatchesDurableOutcome(t *testing.T) {
	for _, max := range []int{1, 2} {
		t.Run(fmt.Sprint(max), func(t *testing.T) {
			s, _ := plainStores(t)
			ctx := context.Background()
			now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
			if err := s.CreateSchedule(ctx, scheduler.Schedule{ID: "failure", Enabled: true, NextRun: now, Retry: scheduler.RetryPolicy{MaxAttempts: max}}); err != nil {
				t.Fatal(err)
			}
			var observed []scheduler.ObserverEvent
			e := scheduler.New(s, runnerFunc(func(context.Context, scheduler.Job) error { return errors.New("dispatch refused") }), scheduler.WithClock(fixedClock{now}), scheduler.WithObserver(scheduler.ObserverFunc(func(_ context.Context, event scheduler.ObserverEvent) error {
				if event.Kind == scheduler.ObserverFailure {
					observed = append(observed, event)
				}
				return nil
			})))
			if tickErr := e.TickNow(ctx); tickErr != nil {
				t.Fatal(tickErr)
			}
			f, _, err := s.GetFire(ctx, scheduler.DeriveFireID("failure", now))
			if err != nil {
				t.Fatal(err)
			}
			if len(observed) != 1 || !reflect.DeepEqual(observed[0].Fire, f) || observed[0].Reason != f.Reason {
				t.Fatalf("observation %+v durable %+v", observed, f)
			}
		})
	}
}

func TestStopDrainsBoundedWorkers(t *testing.T) {
	s, _ := plainStores(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for i := range 8 {
		if err := s.CreateSchedule(ctx, scheduler.Schedule{ID: fmt.Sprint(i), Enabled: true, NextRun: now}); err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var mu sync.Mutex
	active, peak, finished := 0, 0, 0
	e := scheduler.New(s, runnerFunc(func(ctx context.Context, _ scheduler.Job) error {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		mu.Lock()
		active--
		finished++
		mu.Unlock()
		return nil
	}), scheduler.WithClock(fixedClock{now}), scheduler.WithConcurrency(2), scheduler.WithFireTimeout(5*time.Second))
	ticked := make(chan error, 1)
	go func() { ticked <- e.TickNow(ctx) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	stopped := make(chan struct{})
	go func() { e.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned with blocked calls")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not drain")
	}
	if err := <-ticked; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if active != 0 || peak != 2 || e.Status().Dispatches != int64(finished) {
		t.Fatalf("active%d peak%d finished%d status%+v", active, peak, finished, e.Status())
	}
	e.Stop()
}

func TestMisfireCatchUpAcrossDSTPreservesNominalIDs(t *testing.T) {
	s, _ := plainStores(t)
	ctx := context.Background()
	first := time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if err := s.CreateSchedule(ctx, scheduler.Schedule{ID: "dst", Enabled: true, NextRun: first, CronExpr: "30 1 * * *", Location: "America/Los_Angeles", Misfire: scheduler.MisfireRunAll, Overlap: scheduler.OverlapAllow}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	e := scheduler.New(s, runnerFunc(func(_ context.Context, j scheduler.Job) error {
		mu.Lock()
		seen[j.FireID] = true
		mu.Unlock()
		return nil
	}), scheduler.WithClock(fixedClock{now}))
	if err := e.TickNow(ctx); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{first, time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC), time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)} {
		if !seen[scheduler.DeriveFireID("dst", at)] {
			t.Fatalf("missing nominal occurrence%s", at)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("catchup%d expected3 distinct wallclock occurrences", len(seen))
	}
	if seen[scheduler.DeriveFireID("dst", first.Add(time.Hour))] {
		t.Fatal("repeated DST hour dispatched twice")
	}
}

type admissionClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *admissionClock) Now() time.Time                           { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *admissionClock) NewTicker(time.Duration) scheduler.Ticker { panic("TickNow clock") }
func (c *admissionClock) advance(d time.Duration)                  { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

func TestWaitingWorkerClaimsAtActualAdmissionTime(t *testing.T) {
	s, _ := plainStores(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	clock := &admissionClock{now: now}
	for i, at := range []time.Time{now.Add(-time.Minute), now} {
		if err := s.CreateSchedule(ctx, scheduler.Schedule{ID: fmt.Sprint(i), Enabled: true, NextRun: at}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []scheduler.Job
	e := scheduler.New(s, runnerFunc(func(_ context.Context, j scheduler.Job) error {
		seen = append(seen, j)
		if len(seen) == 1 {
			clock.advance(time.Hour)
		} else {
			f, ok, err := s.GetFire(ctx, j.FireID)
			if err != nil || !ok || !j.FiredAt.Equal(clock.Now()) || !f.ClaimExpiresAt.After(clock.Now()) {
				t.Errorf("stale admission job%+v fire%+v err%v", j, f, err)
			}
		}
		return nil
	}), scheduler.WithClock(clock), scheduler.WithConcurrency(1), scheduler.WithClaimLease(time.Minute), scheduler.WithFireTimeout(time.Second))
	if err := e.TickNow(ctx); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !seen[1].FiredAt.Equal(now.Add(time.Hour)) || seen[1].FireID != scheduler.DeriveFireID("1", now) {
		t.Fatalf("waiting worker %+v", seen)
	}
}
