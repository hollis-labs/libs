package retention_test

import (
	"testing"
	"time"

	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention"
)

var now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func plan(t *testing.T, c retention.Config) retention.Plan {
	t.Helper()
	return retention.NewPlanner(mustResolve(t, c)).Plan(now)
}

func tomb(pl retention.Plan, class string, pii bool) (retention.TombstoneSpec, bool) {
	for _, s := range pl.Tombstones {
		if s.Class == class && s.PII == pii && !s.Secret {
			return s, true
		}
	}
	return retention.TombstoneSpec{}, false
}

func TestPlanDefaultsMatchD7(t *testing.T) {
	pl := plan(t, retention.Config{})
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	// Audit: whole rows forever, so no ordinary tombstone and no trim; pii payload capped at 30d.
	if _, ok := tomb(pl, retention.ClassAudit, false); ok {
		t.Error("audit must have no ordinary tombstone")
	}
	if s, ok := tomb(pl, retention.ClassAudit, true); !ok || !s.Before.Equal(ago(30*retention.Day)) || !s.Withhold {
		t.Errorf("audit pii tombstone = %+v, %v; want withheld at 30d", s, ok)
	}
	// State and activity: full age, then envelope age, then trim.
	for class, want := range map[string][2]time.Duration{
		retention.ClassState:    {180 * retention.Day, 2 * retention.Year},
		retention.ClassActivity: {30 * retention.Day, 180 * retention.Day},
		retention.ClassDebug:    {7 * retention.Day, 7 * retention.Day},
	} {
		if s, ok := tomb(pl, class, false); !ok || !s.Before.Equal(ago(want[0])) || s.Withhold {
			t.Errorf("%s tombstone = %+v, %v", class, s, ok)
		}
		found := false
		for _, tr := range pl.Trims {
			if tr.Class == class {
				found = true
				if !tr.Before.Equal(ago(want[1])) {
					t.Errorf("%s trim before %v, want %v", class, tr.Before, ago(want[1]))
				}
			}
		}
		if !found {
			t.Errorf("%s has no trim spec", class)
		}
	}
	// The pii cap never lengthens: debug (7d) stays 7d; state is capped to 30d.
	if s, _ := tomb(pl, retention.ClassDebug, true); !s.Before.Equal(ago(7 * retention.Day)) {
		t.Errorf("debug pii cap = %v, want 7d (the shorter one)", s.Before)
	}
	if s, _ := tomb(pl, retention.ClassState, true); !s.Before.Equal(ago(30 * retention.Day)) {
		t.Errorf("state pii cap = %v, want 30d", s.Before)
	}
	// Audit is never trimmed: it has no trim spec.
	for _, tr := range pl.Trims {
		if tr.Class == retention.ClassAudit {
			t.Error("audit must never be planned for trim")
		}
	}
	// Secret: tombstoned at once, once per policy.
	secrets := 0
	for _, s := range pl.Tombstones {
		if s.Secret {
			secrets++
			if !s.Before.Equal(now) || !s.Withhold {
				t.Errorf("secret spec = %+v", s)
			}
		}
	}
	if secrets != 1 {
		t.Errorf("secret specs = %d, want 1", secrets)
	}
	// Rollups: every configured bucket is planned for its class; pruning only where keep is finite.
	var auditRollups, auditPrunes int
	for _, r := range pl.Rollups {
		if r.Class == retention.ClassAudit {
			auditRollups++
			if len(r.Buckets) != 1 || r.Buckets[0] != retention.D(retention.Day) || !r.ClosedBefore.Equal(now) {
				t.Errorf("audit rollup = %+v", r)
			}
		}
		if r.Class == retention.ClassState && (len(r.Buckets) != 2 || r.Buckets[0] != retention.D(time.Hour) || r.Buckets[1] != retention.D(retention.Day)) {
			t.Errorf("state rollup buckets = %v, want [1h 1d] ascending in one spec", r.Buckets)
		}
	}
	for _, pr := range pl.Prunes {
		if pr.Class == retention.ClassAudit {
			auditPrunes++
		}
	}
	if auditRollups != 1 || auditPrunes != 0 {
		t.Errorf("audit rollups %d prunes %d, want 1 and 0 (1d: forever)", auditRollups, auditPrunes)
	}
	for _, pr := range pl.Prunes {
		if pr.Class == retention.ClassState && pr.Bucket == retention.D(time.Hour) && !pr.Before.Equal(ago(2*retention.Year)) {
			t.Errorf("state 1h prune before %v", pr.Before)
		}
	}
}

func TestPlanForeverPIIEverywhereWhenCapDisabled(t *testing.T) {
	forever := retention.Forever
	c := retention.Config{Acknowledge: true, Overrides: &retention.Overrides{PII: retention.PIIOverride{PayloadMax: &forever}}}
	pl := plan(t, c)
	if _, ok := tomb(pl, retention.ClassAudit, true); ok {
		t.Error("with an unlimited pii cap, audit pii rows have no tombstone")
	}
	// A finite class Full still bounds pii rows.
	if s, ok := tomb(pl, retention.ClassActivity, true); !ok || !s.Before.Equal(now.Add(-30*retention.Day)) {
		t.Errorf("activity pii = %+v, %v", s, ok)
	}
}

// D7 rev 2.2: R-audit rows are never head-trimmed, and a finite audit Full
// tombstones the payload only. Configuration cannot change that.
func TestAuditIsNeverTrimmedEvenWhenConfiguredFinite(t *testing.T) {
	c := retention.Config{Acknowledge: true, Classes: map[string]retention.ClassPolicy{
		retention.ClassAudit: {Full: retention.D(400 * retention.Day), Envelope: retention.D(500 * retention.Day),
			Rollups: []retention.Rollup{{Bucket: retention.D(retention.Day)}}}}}
	pl := plan(t, c)
	for _, tr := range pl.Trims {
		if tr.Class == retention.ClassAudit {
			t.Fatalf("audit planned for trim: %+v", tr)
		}
	}
	if s, ok := tomb(pl, retention.ClassAudit, false); !ok || !s.Before.Equal(now.Add(-400*retention.Day)) || s.Withhold {
		t.Fatalf("a finite audit Full must tombstone the payload at 400d: %+v, %v", s, ok)
	}
}

// The current configuration governs every row: planning depends on nothing else.
func TestPlanIsDeterministic(t *testing.T) {
	a, b := plan(t, retention.Config{}), plan(t, retention.Config{})
	if len(a.Tombstones) != len(b.Tombstones) {
		t.Fatal("plans differ")
	}
	for i := range a.Tombstones {
		if a.Tombstones[i] != b.Tombstones[i] {
			t.Fatalf("tombstone %d differs: %+v vs %+v", i, a.Tombstones[i], b.Tombstones[i])
		}
	}
}

func TestNilPolicyPlansNothing(t *testing.T) {
	pl := retention.NewPlanner(nil).Plan(now)
	if len(pl.Tombstones)+len(pl.Trims)+len(pl.Rollups)+len(pl.Prunes) != 0 {
		t.Fatalf("plan = %+v", pl)
	}
}
