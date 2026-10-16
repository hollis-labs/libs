package retention

import "time"

// RollupSpec says: count rows of Class into every one of Buckets (hour, day,
// ...) once the LARGEST bucket they fall in has closed (its end is at or
// before ClosedBefore). A row is counted into all buckets at once and then
// marked rolled up, so a row is never counted twice and a trim never sees a
// half-rolled row.
type RollupSpec struct {
	Class        string
	Buckets      []Duration // ascending
	ClosedBefore time.Time
}

// TombstoneSpec says: remove the payload of rows of Class recorded at or
// before Before, keeping the envelope and a payload digest. PII selects the
// rows flagged pii (their cap is shorter); Withhold means the stored digest is
// "withheld" instead of a sha256 of the payload. Secret selects rows with
// sensitivity=secret, which are never meant to be stored and are tombstoned at
// once; Class is empty for it.
type TombstoneSpec struct {
	Class    string
	PII      bool
	Secret   bool
	Withhold bool
	Before   time.Time
}

// TrimSpec says: rows of Class recorded at or before Before are past their
// envelope age and may leave the head of the stream, once they are rolled up.
// Trim is head-only unless the store declares otherwise. R-audit never has one.
type TrimSpec struct {
	Class  string
	Before time.Time
	// RequireRolled is true when the class has rollups configured: a row may
	// only be trimmed after it was counted. A class without rollups has
	// nothing to count and is trimmable on age alone.
	RequireRolled bool
}

// PruneSpec says: drop rollup buckets of Class/Bucket that ended at or
// before Before.
type PruneSpec struct {
	Class  string
	Bucket Duration
	Before time.Time
}

// Plan is what a pass should do at Now, as set operations over classes and
// cutoff times. It names no row, so any store (SQLite, PostgreSQL, a hub's own
// tables) can run it with one statement per spec. The current configuration
// governs every row: a shorter age reaches rows that already exist on the
// next pass, a longer one never restores anything already tombstoned or
// trimmed (D7 rev 2.2). A row's admitted policy id is kept for audit only and
// plays no part here.
type Plan struct {
	Now        time.Time
	Rollups    []RollupSpec
	Tombstones []TombstoneSpec
	Trims      []TrimSpec
	Prunes     []PruneSpec
}

// Planner turns a policy into [Plan]s. It does no I/O and is deterministic.
type Planner struct{ policy *Policy }

// NewPlanner returns a Planner for the current policy. A nil policy plans nothing.
func NewPlanner(p *Policy) *Planner { return &Planner{policy: p} }

// ClassOf classifies attributes with the policy. It is false for a nil policy.
func (pl *Planner) ClassOf(a Attrs) (string, bool) {
	if pl == nil || pl.policy == nil {
		return "", false
	}
	return pl.policy.ClassOf(a), true
}

var classOrder = []string{ClassAudit, ClassState, ClassActivity, ClassDebug}

// Plan returns the work at now.
func (pl *Planner) Plan(now time.Time) Plan {
	out := Plan{Now: now}
	if pl == nil || pl.policy == nil {
		return out
	}
	p := pl.policy

	// Secret payloads are never stored; if one is found it goes at once.
	out.Tombstones = append(out.Tombstones, TombstoneSpec{Secret: true, Withhold: true, Before: now})

	for _, name := range classOrder {
		c, ok := p.Classes[name]
		if !ok {
			continue
		}
		// Payload age for ordinary rows: the class's Full (zero = forever). In
		// R-audit a finite Full tombstones the payload only; the envelope stays.
		if c.Full != 0 {
			out.Tombstones = append(out.Tombstones, TombstoneSpec{Class: name, Before: now.Add(-time.Duration(c.Full))})
		}
		// PII rows: the shorter of the class's Full and the pii cap, in every
		// class including R-audit; the payload only, the envelope follows the class.
		pii := p.PIIMax
		if c.Full != 0 && (pii == 0 || c.Full < pii) {
			pii = c.Full
		}
		if pii != 0 {
			out.Tombstones = append(out.Tombstones, TombstoneSpec{Class: name, PII: true, Withhold: true, Before: now.Add(-time.Duration(pii))})
		}
		// Head trim: only classes with a finite whole-row age and a finite
		// envelope age, and never R-audit, whatever its configuration.
		if name != ClassAudit && c.Full != 0 && c.Envelope != 0 {
			out.Trims = append(out.Trims, TrimSpec{Class: name, Before: now.Add(-time.Duration(c.Envelope)), RequireRolled: len(c.Rollups) > 0})
		}
		if len(c.Rollups) > 0 {
			buckets := make([]Duration, 0, len(c.Rollups))
			for _, r := range c.Rollups {
				buckets = append(buckets, r.Bucket)
			}
			sortDurations(buckets)
			out.Rollups = append(out.Rollups, RollupSpec{Class: name, Buckets: buckets, ClosedBefore: now})
		}
		for _, r := range c.Rollups {
			if r.Keep != 0 {
				out.Prunes = append(out.Prunes, PruneSpec{Class: name, Bucket: r.Bucket, Before: now.Add(-time.Duration(r.Keep))})
			}
		}
	}
	return out
}

func sortDurations(d []Duration) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j] < d[j-1]; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}
