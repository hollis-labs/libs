package retention_test

import (
	"testing"
	"time"

	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention"
)

var now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func plan(t *testing.T, c retention.Config, ids ...string) retention.Plan {
	t.Helper()
	p := mustResolve(t, c)
	if len(ids) == 0 {
		ids = []string{""}
	}
	return retention.NewPlanner(retention.CurrentOnly(p)).Plan(now, ids)
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
			if r.Bucket != retention.D(retention.Day) || !r.ClosedBefore.Equal(now) {
				t.Errorf("audit rollup = %+v", r)
			}
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

func TestPlanIsDeterministicAndPerPolicy(t *testing.T) {
	old := mustResolve(t, retention.Config{Classes: map[string]retention.ClassPolicy{
		retention.ClassActivity: {Full: retention.D(10 * retention.Day), Envelope: retention.D(20 * retention.Day)}}})
	cur := mustResolve(t, retention.Config{})
	res := func(id string) (*retention.Policy, bool) {
		switch id {
		case "", "v2":
			return cur, true
		case "v1":
			return old, true
		}
		return nil, false
	}
	pl := retention.NewPlanner(res).Plan(now, []string{"v2", "v1", "", "v1", "gone"})
	if len(pl.Unresolved) != 1 || pl.Unresolved[0] != "gone" {
		t.Fatalf("unresolved = %v; an unknown policy id must be reported, never guessed", pl.Unresolved)
	}
	// Each admitted policy keeps its own cutoffs.
	got := map[string]time.Time{}
	for _, s := range pl.Tombstones {
		if s.Class == retention.ClassActivity && !s.PII && !s.Secret {
			got[s.PolicyID] = s.Before
		}
	}
	if !got["v1"].Equal(now.Add(-10*retention.Day)) || !got["v2"].Equal(now.Add(-30*retention.Day)) || !got[""].Equal(now.Add(-30*retention.Day)) {
		t.Fatalf("per-policy cutoffs = %v", got)
	}
	again := retention.NewPlanner(res).Plan(now, []string{"", "gone", "v1", "v2"})
	if len(again.Tombstones) != len(pl.Tombstones) || len(again.Trims) != len(pl.Trims) {
		t.Fatal("plan depends on the order of the policy ids")
	}
	for i := range pl.Tombstones {
		if pl.Tombstones[i] != again.Tombstones[i] {
			t.Fatalf("tombstone %d differs: %+v vs %+v", i, pl.Tombstones[i], again.Tombstones[i])
		}
	}
}

func TestNilPlannerLeavesEverythingUntouched(t *testing.T) {
	pl := retention.NewPlanner(nil).Plan(now, []string{""})
	if len(pl.Tombstones)+len(pl.Trims)+len(pl.Rollups)+len(pl.Prunes) != 0 || len(pl.Unresolved) != 1 {
		t.Fatalf("plan = %+v", pl)
	}
}
