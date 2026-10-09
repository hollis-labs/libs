package conformance_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	scheduler "github.com/hollis-labs/libs/util/scheduler"
)

// memStore is an in-memory scheduler.Store used to prove the suite itself.
// The dropFiredAt and dropClaimFence switches each introduce one classic
// defect that the suite must detect.
type memStore struct {
	mu        sync.Mutex
	schedules map[string]scheduler.Schedule
	fires     map[string]scheduler.Fire
	pruned    map[string]time.Time

	// dropFiredAt makes ClaimFire ignore ExpectedFiredAt (the CAS that omits
	// it from its precondition).
	dropFiredAt bool
	// dropClaimFence makes TransitionFire ignore ClaimedAt.
	dropClaimFence bool
	// relabelExpired makes ListDueFires rewrite expired claims to retrying.
	relabelExpired bool
}

func newMemStore() *memStore {
	return &memStore{schedules: map[string]scheduler.Schedule{}, fires: map[string]scheduler.Fire{}}
}

func (m *memStore) CreateSchedule(_ context.Context, s scheduler.Schedule) error {
	if err := scheduler.ValidateSchedule(s); err != nil {
		return err
	}
	if err := scheduler.ValidatePolicies(s); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.schedules[s.ID]; ok {
		return errors.New("exists")
	}
	m.schedules[s.ID] = s
	return nil
}

func (m *memStore) ListDueSchedules(_ context.Context, now time.Time, limit int) ([]scheduler.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []scheduler.Schedule
	for _, s := range m.schedules {
		if s.Enabled && !s.NextRun.IsZero() && !s.NextRun.After(now) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) CreateFire(_ context.Context, c scheduler.FireCreation) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.schedules[c.ScheduleID]
	if !ok || !s.Enabled || !s.NextRun.Equal(c.ExpectedNext) {
		return false, nil
	}
	if through, ok := m.pruned[c.ScheduleID]; ok && !c.Fire.ScheduledAt.After(through) {
		return false, nil
	}
	if _, dup := m.fires[c.Fire.ID]; dup {
		return false, nil
	}
	count := 0
	for _, existing := range m.fires {
		if existing.ScheduleID == c.ScheduleID && (existing.Status == scheduler.FirePending || existing.Status == scheduler.FireRetrying) {
			count++
		}
	}
	limit := c.Fire.MaxQueuedFires
	if limit == 0 {
		limit = 100
	}
	if c.Fire.Overlap == scheduler.OverlapQueue && c.Fire.Status == scheduler.FirePending && count >= limit {
		return false, scheduler.ErrQueueFull
	}
	m.fires[c.Fire.ID] = c.Fire
	s.LastRun, s.NextRun = c.Fire.ScheduledAt, c.NextRun
	m.schedules[s.ID] = s
	return true, nil
}

func (m *memStore) ListDueFires(_ context.Context, now time.Time, limit int) ([]scheduler.Fire, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []scheduler.Fire
	for _, f := range m.fires {
		ready := (f.Status == scheduler.FirePending || f.Status == scheduler.FireRetrying) && !f.NextAttemptAt.After(now)
		expired := f.Status == scheduler.FireClaimed && !f.ClaimExpiresAt.After(now)
		if f.Overlap == scheduler.OverlapQueue {
			blocked := false
			for _, sibling := range m.fires {
				if sibling.ID != f.ID && sibling.ScheduleID == f.ScheduleID && sibling.Status == scheduler.FireClaimed && sibling.ClaimExpiresAt.After(now) {
					blocked = true
					break
				}
			}
			if blocked {
				continue
			}
		}
		if ready || expired {
			if expired && m.relabelExpired {
				f.Status = scheduler.FireRetrying
			}
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) ClaimFire(_ context.Context, c scheduler.FireClaim) (scheduler.Fire, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.fires[c.FireID]
	if !ok || f.Status != c.ExpectedStatus || f.Attempt != c.ExpectedAttempt {
		return scheduler.Fire{}, false, nil
	}
	if !m.dropFiredAt && !f.FiredAt.Equal(c.ExpectedFiredAt) {
		return scheduler.Fire{}, false, nil
	}
	if f.Overlap != scheduler.OverlapAllow {
		for _, other := range m.fires {
			if other.ID != f.ID && other.ScheduleID == f.ScheduleID && other.Status == scheduler.FireClaimed && other.ClaimExpiresAt.After(c.ClaimedAt) {
				return scheduler.Fire{}, false, scheduler.ErrScheduleBusy
			}
		}
	}
	recovering := f.Status == scheduler.FireClaimed
	if recovering && f.ClaimExpiresAt.After(c.ClaimedAt) {
		return scheduler.Fire{}, false, nil
	}
	if !recovering && f.Status != scheduler.FirePending && f.Status != scheduler.FireRetrying {
		return scheduler.Fire{}, false, nil
	}
	f.Status = scheduler.FireClaimed
	if !recovering {
		f.Attempt++
	}
	f.FiredAt, f.ClaimExpiresAt, f.NextAttemptAt = c.ClaimedAt, c.ClaimExpiresAt, time.Time{}
	m.fires[f.ID] = f
	return f, true, nil
}

func (m *memStore) TransitionFire(_ context.Context, t scheduler.FireTransition) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.fires[t.FireID]
	if !ok || f.Status != t.From || f.Attempt != t.Attempt || (!m.dropClaimFence && !f.FiredAt.Equal(t.ClaimedAt)) {
		return false, nil
	}
	f.Status, f.ClaimExpiresAt, f.NextAttemptAt, f.LastError = t.To, time.Time{}, t.NextAttemptAt, t.Error
	f.Reason = t.Reason
	m.fires[f.ID] = f
	return true, nil
}

func (m *memStore) DisableSchedule(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.schedules[id]
	if !ok {
		return errors.New("not found")
	}
	s.Enabled = false
	m.schedules[id] = s
	return nil
}

func (m *memStore) Prune(ctx context.Context, olderThan time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pruned == nil {
		m.pruned = map[string]time.Time{}
	}
	keep := map[string]int{}
	for id, s := range m.schedules {
		keep[id] = s.KeepLastN
	}
	fires := make([]scheduler.Fire, 0, len(m.fires))
	for _, f := range m.fires {
		fires = append(fires, f)
	}
	candidates := scheduler.PrunableFires(fires, keep, olderThan)
	for _, f := range candidates {
		if f.ScheduledAt.After(m.pruned[f.ScheduleID]) {
			m.pruned[f.ScheduleID] = f.ScheduledAt
		}
		delete(m.fires, f.ID)
	}
	return len(candidates), nil
}
