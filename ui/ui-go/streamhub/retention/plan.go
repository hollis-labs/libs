package retention

import (
	"slices"
	"sort"
	"time"
)

// RollupSpec says: build Bucket-sized rollup buckets for rows of Class whose
// bucket has closed (its end is at or before ClosedBefore).
type RollupSpec struct {
	PolicyID     string
	Class        string
	Bucket       Duration
	ClosedBefore time.Time
}

// TombstoneSpec says: remove the payload of rows of Class recorded at or
// before Before, keeping the envelope and a payload digest. PII selects the
// rows flagged pii (their cap is shorter); Withhold means the stored digest is
// "withheld" instead of a sha256 of the payload. Secret selects rows with
// sensitivity=secret, which are never meant to be stored and are tombstoned at
// once; Class is empty for it.
type TombstoneSpec struct {
	PolicyID string
	Class    string
	PII      bool
	Secret   bool
	Withhold bool
	Before   time.Time
}

// TrimSpec says: rows of Class recorded at or before Before are past their
// envelope age and may leave the head of the stream, once they are rolled up.
// Trim is head-only: a store stops at the first row that is not eligible.
type TrimSpec struct {
	PolicyID string
	Class    string
	Before   time.Time
}

// PruneSpec says: drop rollup buckets of Class/Bucket that ended at or
// before Before.
type PruneSpec struct {
	PolicyID string
	Class    string
	Bucket   Duration
	Before   time.Time
}

// Plan is what a pass should do at Now, as set operations over classes and
// cutoff times. It names no row, so any store (SQLite, PostgreSQL, a hub's own
// tables) can run it with one statement per spec.
type Plan struct {
	Now        time.Time
	Rollups    []RollupSpec
	Tombstones []TombstoneSpec
	Trims      []TrimSpec
	Prunes     []PruneSpec
	// Unresolved lists policy ids the resolver did not know. Their rows are
	// left untouched: an unknown policy is never guessed.
	Unresolved []string
}

// Resolver maps a policy id carried on a row to a policy. The empty id means
// "the current policy". Return false for an id that is no longer known.
type Resolver func(policyID string) (*Policy, bool)

// Planner turns policies into [Plan]s. It does no I/O and is deterministic.
type Planner struct{ resolve Resolver }

// NewPlanner returns a Planner. A nil resolver is an error at Plan time, not
// a panic: every id is then unresolved.
func NewPlanner(resolve Resolver) *Planner { return &Planner{resolve: resolve} }

// CurrentOnly is a [Resolver] that knows only the current policy: every row is
// governed by p, whatever id it carries. This is D7 rev 2.1's reading, where a
// configuration change reaches existing rows on the next pass.
func CurrentOnly(p *Policy) Resolver { return func(string) (*Policy, bool) { return p, p != nil } }

// Plan returns the work for the given policy ids (the distinct values found
// on rows; include "" for rows with none). Ids are planned independently so
// that rows keep the policy they were admitted under when the caller uses a
// snapshot resolver.
func (pl *Planner) Plan(now time.Time, policyIDs []string) Plan {
	out := Plan{Now: now}
	ids := slices.Clone(policyIDs)
	sort.Strings(ids)
	ids = slices.Compact(ids)
	for _, id := range ids {
		var p *Policy
		ok := false
		if pl != nil && pl.resolve != nil {
			p, ok = pl.resolve(id)
		}
		if !ok || p == nil {
			out.Unresolved = append(out.Unresolved, id)
			continue
		}
		out.add(id, p, now)
	}
	return out
}

var classOrder = []string{ClassAudit, ClassState, ClassActivity, ClassDebug}

func (out *Plan) add(id string, p *Policy, now time.Time) {
	// Secret payloads are never stored; if one is found it goes at once.
	out.Tombstones = append(out.Tombstones, TombstoneSpec{PolicyID: id, Secret: true, Withhold: true, Before: now})

	for _, name := range classOrder {
		c, ok := p.Classes[name]
		if !ok {
			continue
		}
		// Payload age for ordinary rows: the class's Full (zero = forever).
		if c.Full != 0 {
			out.Tombstones = append(out.Tombstones, TombstoneSpec{PolicyID: id, Class: name, Before: now.Add(-time.Duration(c.Full))})
		}
		// PII rows: the shorter of the class's Full and the pii cap, in every
		// class including R-audit; the payload only, the envelope follows the class.
		pii := p.PIIMax
		if c.Full != 0 && (pii == 0 || c.Full < pii) {
			pii = c.Full
		}
		if pii != 0 {
			out.Tombstones = append(out.Tombstones, TombstoneSpec{PolicyID: id, Class: name, PII: true, Withhold: true, Before: now.Add(-time.Duration(pii))})
		}
		// Head trim: only classes with a finite whole-row age and a finite envelope age.
		if c.Full != 0 && c.Envelope != 0 {
			out.Trims = append(out.Trims, TrimSpec{PolicyID: id, Class: name, Before: now.Add(-time.Duration(c.Envelope))})
		}
		for _, r := range c.Rollups {
			out.Rollups = append(out.Rollups, RollupSpec{PolicyID: id, Class: name, Bucket: r.Bucket, ClosedBefore: now})
			if r.Keep != 0 {
				out.Prunes = append(out.Prunes, PruneSpec{PolicyID: id, Class: name, Bucket: r.Bucket, Before: now.Add(-time.Duration(r.Keep))})
			}
		}
	}
}
