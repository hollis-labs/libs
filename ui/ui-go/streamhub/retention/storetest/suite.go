package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention"
)

var ctx = context.Background()

// t0 is the fixed origin of the suite's timeline; rows are placed relative to it.
var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func classOf(a retention.Attrs) string {
	switch a.Category {
	case "audit":
		return retention.ClassAudit
	case "state":
		return retention.ClassState
	}
	return retention.ClassActivity
}

func add(t *testing.T, f Fixture, r Row) uint64 {
	t.Helper()
	if r.Stream == "" {
		r.Stream = "s"
	}
	seq, err := f.Add(r)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return seq
}

func row(class string, at time.Duration, payload string) Row {
	return Row{Class: class, At: t0.Add(at), Payload: []byte(payload), Attrs: retention.Attrs{Category: "state"}}
}

func byClass(rows []RowState, seq uint64) RowState {
	for _, r := range rows {
		if r.Seq == seq {
			return r
		}
	}
	return RowState{}
}

// suiteError carries a store error out of a subtest; guard turns it into t.Fatalf.
type suiteError struct{ err error }

// rollAll rolls up every row of every class, so a per-row store (which never
// removes an unrolled row) may trim them.
func rollAll(t *testing.T, f Fixture) {
	t.Helper()
	for _, c := range []string{retention.ClassAudit, retention.ClassState, retention.ClassActivity, retention.ClassDebug} {
		for {
			r, err := f.Store().Rollup(ctx, "s", retention.RollupSpec{Class: c, Buckets: []retention.Duration{retention.D(time.Hour)}, ClosedBefore: t0.Add(5000 * 24 * time.Hour)}, retention.Op{})
			if err != nil {
				t.Fatalf("rollAll: %v", err)
			}
			if !r.More {
				break
			}
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(suiteError{err})
	}
	return v
}

func guard(f func(t *testing.T)) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				if se, ok := r.(suiteError); ok {
					t.Fatalf("unexpected store error: %v", se.err)
				}
				panic(r)
			}
		}()
		f(t)
	}
}

// Run runs the conformance suite against the store newFixture builds. Each
// subtest gets a fresh, empty fixture.
func Run(t *testing.T, newFixture func(t *testing.T) Fixture) {
	t.Helper()
	day := 24 * time.Hour

	t.Run("ClassifyFillsMissingClassesOnce", guard(func(t *testing.T) {
		f := newFixture(t)
		a := add(t, f, Row{At: t0, Attrs: retention.Attrs{Category: "audit"}})
		b := add(t, f, Row{At: t0, Attrs: retention.Attrs{Category: "tool"}})
		c := add(t, f, row(retention.ClassState, 0, "x")) // already classified: untouched
		st := f.Store()
		if r := must(st.Classify(ctx, "s", classOf, retention.Op{DryRun: true})); r.Rows != 2 {
			t.Fatalf("dry run counted %d, want 2", r.Rows)
		}
		if got := f.Rows("s"); byClass(got, a).Class != "" {
			t.Fatal("a dry run wrote")
		}
		if r := must(st.Classify(ctx, "s", classOf, retention.Op{})); r.Rows != 2 || r.More {
			t.Fatalf("Classify = %+v", r)
		}
		got := f.Rows("s")
		if byClass(got, a).Class != retention.ClassAudit || byClass(got, b).Class != retention.ClassActivity || byClass(got, c).Class != retention.ClassState {
			t.Fatalf("classes = %+v", got)
		}
		if r := must(st.Classify(ctx, "s", classOf, retention.Op{})); r.Rows != 0 {
			t.Fatalf("second Classify changed %d rows", r.Rows)
		}
	}))

	t.Run("TombstoneKeepsEnvelopeAndSetsDigest", guard(func(t *testing.T) {
		f := newFixture(t)
		old := add(t, f, row(retention.ClassState, 0, "old payload"))
		fresh := add(t, f, row(retention.ClassState, 40*day, "fresh payload"))
		st := f.Store()
		spec := retention.TombstoneSpec{Class: retention.ClassState, Before: t0.Add(10 * day)}
		if r := must(st.Tombstone(ctx, "s", spec, retention.Op{DryRun: true})); r.Rows != 1 {
			t.Fatalf("dry run = %+v", r)
		}
		if byClass(f.Rows("s"), old).Tombstoned {
			t.Fatal("a dry run wrote")
		}
		r := must(st.Tombstone(ctx, "s", spec, retention.Op{}))
		if r.Rows != 1 || r.BytesFreed != int64(len("old payload")) {
			t.Fatalf("Tombstone = %+v", r)
		}
		got := f.Rows("s")
		o, n := byClass(got, old), byClass(got, fresh)
		sum := sha256.Sum256([]byte("old payload"))
		if !o.Tombstoned || o.Payload != nil || o.PayloadDigest != "sha256:"+hex.EncodeToString(sum[:]) {
			t.Fatalf("tombstoned row = %+v", o)
		}
		if o.Class != retention.ClassState || !o.At.Equal(t0) || o.Seq != old {
			t.Fatalf("the envelope (cursor, class, time) must survive: %+v", o)
		}
		if n.Tombstoned || string(n.Payload) != "fresh payload" {
			t.Fatalf("a row inside its age was touched: %+v", n)
		}
		if r := must(st.Tombstone(ctx, "s", spec, retention.Op{})); r.Rows != 0 {
			t.Fatalf("second Tombstone changed %d rows", r.Rows)
		}
	}))

	t.Run("TombstonePIIWithholdsAndSecretGoesAtOnce", guard(func(t *testing.T) {
		f := newFixture(t)
		plain := add(t, f, row(retention.ClassState, 0, "plain"))
		pii := row(retention.ClassState, 0, "personal")
		pii.Attrs.PII = true
		p := add(t, f, pii)
		sec := row(retention.ClassActivity, 100*day, "token")
		sec.Attrs = retention.Attrs{Category: "tool", Sensitivity: "secret"}
		s := add(t, f, sec)
		st := f.Store()
		// The ordinary spec leaves pii rows to their own spec.
		must(st.Tombstone(ctx, "s", retention.TombstoneSpec{Class: retention.ClassState, Before: t0.Add(day)}, retention.Op{}))
		got := f.Rows("s")
		if !byClass(got, plain).Tombstoned || byClass(got, p).Tombstoned {
			t.Fatalf("ordinary spec: plain %v pii %v", byClass(got, plain).Tombstoned, byClass(got, p).Tombstoned)
		}
		must(st.Tombstone(ctx, "s", retention.TombstoneSpec{Class: retention.ClassState, PII: true, Withhold: true, Before: t0.Add(day)}, retention.Op{}))
		if g := byClass(f.Rows("s"), p); !g.Tombstoned || g.PayloadDigest != "withheld" || g.Payload != nil {
			t.Fatalf("pii row = %+v", g)
		}
		// A secret spec reaches any class, whatever its age.
		must(st.Tombstone(ctx, "s", retention.TombstoneSpec{Secret: true, Withhold: true, Before: t0.Add(101 * day)}, retention.Op{}))
		if g := byClass(f.Rows("s"), s); !g.Tombstoned || g.PayloadDigest != "withheld" {
			t.Fatalf("secret row = %+v", g)
		}
	}))

	t.Run("TombstoneRespectsLimitAndIgnoresAdmittedPolicyID", guard(func(t *testing.T) {
		f := newFixture(t)
		for range 4 {
			add(t, f, row(retention.ClassState, 0, "x"))
		}
		other := row(retention.ClassState, 0, "keep")
		other.PolicyID = "v1"
		o := add(t, f, other)
		st := f.Store()
		spec := retention.TombstoneSpec{Class: retention.ClassState, Before: t0.Add(day)}
		total, calls := int64(0), 0
		for {
			r := must(st.Tombstone(ctx, "s", spec, retention.Op{Limit: 2}))
			if r.Rows > 2 {
				t.Fatalf("Limit 2 changed %d rows", r.Rows)
			}
			total += r.Rows
			calls++
			if !r.More {
				break
			}
			if calls > 10 {
				t.Fatal("More never cleared")
			}
		}
		if total != 5 || calls != 3 {
			t.Fatalf("total %d in %d calls, want 5 in 3", total, calls)
		}
		// D7 rev 2.2: the admitted policy id is for audit only; the current config governs every row.
		if !byClass(f.Rows("s"), o).Tombstoned {
			t.Fatal("a row admitted under another policy id was shielded from the current configuration")
		}
	}))

	t.Run("RollupCountsClosedBucketsOnce", guard(func(t *testing.T) {
		f := newFixture(t)
		h := time.Hour
		mk := func(at time.Duration, kind string) uint64 {
			r := row(retention.ClassState, at, "p")
			r.Dims = Dims{Kind: kind, Project: "p1"}
			return add(t, f, r)
		}
		a := mk(5*time.Minute, "k1")
		b := mk(20*time.Minute, "k1")
		c := mk(30*time.Minute, "k2")
		open := mk(2*day+30*time.Minute, "k1") // its day is still open
		st := f.Store()
		spec := retention.RollupSpec{Class: retention.ClassState, Buckets: []retention.Duration{retention.D(h), retention.D(day)}, ClosedBefore: t0.Add(day + h)}
		if r := must(st.Rollup(ctx, "s", spec, retention.Op{DryRun: true})); r.Rows != 3 {
			t.Fatalf("dry run = %+v", r)
		}
		if len(f.Buckets("s")) != 0 {
			t.Fatal("a dry run wrote")
		}
		r := must(st.Rollup(ctx, "s", spec, retention.Op{}))
		if r.Rows != 3 || r.More {
			t.Fatalf("Rollup = %+v", r)
		}
		rolled := map[uint64]bool{}
		for _, x := range f.Rows("s") {
			rolled[x.Seq] = x.Rolled
		}
		if !rolled[a] || !rolled[b] || !rolled[c] || rolled[open] {
			t.Fatalf("rolled flags = %v", rolled)
		}
		var hourK1, dayAll *Bucket
		for _, bk := range f.Buckets("s") {
			bk := bk
			switch {
			case bk.Size == retention.D(h) && bk.Dims.Kind == "k1":
				hourK1 = &bk
			case bk.Size == retention.D(day) && bk.Dims.Kind == "k2":
				dayAll = &bk
			}
		}
		if hourK1 == nil || hourK1.Count != 2 || hourK1.FirstSeq != a || hourK1.LastSeq != b || !hourK1.Start.Equal(t0) {
			t.Fatalf("hourly k1 bucket = %+v", hourK1)
		}
		if dayAll == nil || dayAll.Count != 1 || dayAll.FirstSeq != c {
			t.Fatalf("daily k2 bucket = %+v", dayAll)
		}
		// Counting is once per row: a second pass adds nothing, even later.
		spec.ClosedBefore = t0.Add(day + 2*h)
		if r := must(st.Rollup(ctx, "s", spec, retention.Op{})); r.Rows != 0 {
			t.Fatalf("second Rollup counted %d rows", r.Rows)
		}
		if hourK1 = nil; len(f.Buckets("s")) != 4 {
			t.Fatalf("buckets = %+v", f.Buckets("s"))
		}
	}))

	t.Run("TrimNeedsAgeAndRollup", guard(func(t *testing.T) {
		f := newFixture(t)
		a := add(t, f, row(retention.ClassActivity, 0, "a"))
		b := add(t, f, row(retention.ClassActivity, time.Hour, "b"))
		st := f.Store()
		spec := retention.TrimSpec{Class: retention.ClassActivity, Before: t0.Add(10 * day), RequireRolled: true}
		if r := must(st.Trim(ctx, "s", []retention.TrimSpec{spec}, retention.Op{})); r.Rows != 0 {
			t.Fatalf("an unrolled row was trimmed: %+v", r)
		}
		must(st.Rollup(ctx, "s", retention.RollupSpec{Class: retention.ClassActivity, Buckets: []retention.Duration{retention.D(day)}, ClosedBefore: t0.Add(5 * day)}, retention.Op{}))
		spec.Before = t0.Add(30 * time.Minute) // only the first row is old enough
		r := must(st.Trim(ctx, "s", []retention.TrimSpec{spec}, retention.Op{}))
		if r.Rows != 1 || r.ByClass[retention.ClassActivity] != 1 || r.BytesFreed == 0 {
			t.Fatalf("Trim = %+v", r)
		}
		left := f.Rows("s")
		if len(left) != 1 || left[0].Seq != b || byClass(left, a).Seq != 0 {
			t.Fatalf("rows left = %+v", left)
		}
		if f.Store().Caps().Trim == retention.TrimHeadOnly && f.PrunedThrough("s") != a {
			t.Fatalf("pruned through %d, want %d", f.PrunedThrough("s"), a)
		}
		// A class without rollups is trimmable on age alone.
		c := add(t, f, row(retention.ClassDebug, 0, "c"))
		if f.Store().Caps().Trim == retention.TrimHeadOnly {
			// c sits behind b (a different class with no spec): head-only stops there.
			r = must(st.Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}}, retention.Op{}))
			if r.Rows != 0 || r.Held != 1 {
				t.Fatalf("head-only behind an ineligible row: %+v, want 0 trimmed and 1 held", r)
			}
		} else {
			// A per-row store fails closed without a covering rollup, even for a class with none configured.
			r = must(st.Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}}, retention.Op{}))
			if r.Rows != 0 {
				t.Fatalf("per-row trim removed an unrolled row: %+v", r)
			}
			rollAll(t, f)
			r = must(st.Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}}, retention.Op{}))
			if r.Rows != 1 || byClass(f.Rows("s"), c).Seq != 0 {
				t.Fatalf("per-row trim after the rollup: %+v", r)
			}
		}
	}))

	t.Run("HeadOnlyTrimStopsAtFirstIneligibleRow", guard(func(t *testing.T) {
		f := newFixture(t)
		if f.Store().Caps().Trim != retention.TrimHeadOnly {
			t.Skip("store trims per row")
		}
		a := add(t, f, row(retention.ClassDebug, 0, "a"))
		keep := add(t, f, row(retention.ClassAudit, time.Hour, "keep")) // audit: no spec
		c := add(t, f, row(retention.ClassDebug, 2*time.Hour, "c"))
		st := f.Store()
		rollAll(t, f)
		r := must(st.Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}}, retention.Op{}))
		if r.Rows != 1 || r.Held != 1 {
			t.Fatalf("Trim = %+v, want 1 removed and 1 held", r)
		}
		left := f.Rows("s")
		if len(left) != 2 || left[0].Seq != keep || left[1].Seq != c {
			t.Fatalf("rows left = %+v (a=%d)", left, a)
		}
		if f.PrunedThrough("s") != a {
			t.Fatalf("pruned through %d, want %d: nothing past the first kept row may be removed", f.PrunedThrough("s"), a)
		}
	}))

	t.Run("PerRowTrimRemovesEligibleRowsAnywhere", guard(func(t *testing.T) {
		f := newFixture(t)
		if f.Store().Caps().Trim != retention.TrimPerRow {
			t.Skip("store trims head-only")
		}
		add(t, f, row(retention.ClassDebug, 0, "a"))
		keep := add(t, f, row(retention.ClassAudit, time.Hour, "keep"))
		add(t, f, row(retention.ClassDebug, 2*time.Hour, "c"))
		rollAll(t, f)
		r := must(f.Store().Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}}, retention.Op{}))
		if r.Rows != 2 || r.Held != 0 {
			t.Fatalf("Trim = %+v", r)
		}
		if left := f.Rows("s"); len(left) != 1 || left[0].Seq != keep {
			t.Fatalf("rows left = %+v", left)
		}
	}))

	t.Run("PruneDropsExpiredBucketsOnly", guard(func(t *testing.T) {
		f := newFixture(t)
		add(t, f, row(retention.ClassState, 0, "a"))
		add(t, f, row(retention.ClassState, 3*day, "b"))
		st := f.Store()
		must(st.Rollup(ctx, "s", retention.RollupSpec{Class: retention.ClassState, Buckets: []retention.Duration{retention.D(day)}, ClosedBefore: t0.Add(10 * day)}, retention.Op{}))
		if n := len(f.Buckets("s")); n != 2 {
			t.Fatalf("buckets = %d, want 2", n)
		}
		spec := retention.PruneSpec{Class: retention.ClassState, Bucket: retention.D(day), Before: t0.Add(2 * day)}
		if r := must(st.Prune(ctx, "s", spec, retention.Op{DryRun: true})); r.Rows != 1 || len(f.Buckets("s")) != 2 {
			t.Fatalf("dry run = %+v", r)
		}
		if r := must(st.Prune(ctx, "s", spec, retention.Op{})); r.Rows != 1 {
			t.Fatalf("Prune = %+v", r)
		}
		if bs := f.Buckets("s"); len(bs) != 1 || !bs[0].Start.Equal(t0.Add(3*day)) {
			t.Fatalf("buckets left = %+v", bs)
		}
	}))

	t.Run("StatsReportsRemainingRowsAndOldest", guard(func(t *testing.T) {
		f := newFixture(t)
		add(t, f, row(retention.ClassState, 2*time.Hour, "a"))
		add(t, f, row(retention.ClassState, time.Hour, "b"))
		add(t, f, row(retention.ClassAudit, 0, "c"))
		stats := must(f.Store().Stats(ctx, "s"))
		got := map[string]retention.ClassStat{}
		for _, s := range stats {
			got[s.Class] = s
		}
		if got[retention.ClassState].Rows != 2 || !got[retention.ClassState].Oldest.Equal(t0.Add(time.Hour)) ||
			got[retention.ClassAudit].Rows != 1 || !got[retention.ClassAudit].Oldest.Equal(t0) {
			t.Fatalf("stats = %+v", stats)
		}
	}))

	t.Run("LockIsExclusive", guard(func(t *testing.T) {
		f := newFixture(t)
		st := f.Store()
		unlock, ok, err := st.TryLock(ctx)
		if err != nil || !ok {
			t.Fatalf("first TryLock = %v, %v", ok, err)
		}
		if _, ok2, err := st.TryLock(ctx); err != nil || ok2 {
			t.Fatalf("second TryLock = %v, %v; want false without error", ok2, err)
		}
		unlock()
		unlock2, ok3, err := st.TryLock(ctx)
		if err != nil || !ok3 {
			t.Fatalf("TryLock after unlock = %v, %v", ok3, err)
		}
		unlock2()
	}))

	// --- D7 rev 2.3: every precondition of a removal, the watermark, the gap frame ---

	t.Run("TrimNeverRemovesAudit", guard(func(t *testing.T) {
		f := newFixture(t)
		a := add(t, f, row(retention.ClassAudit, 0, "audit"))
		d := add(t, f, row(retention.ClassDebug, time.Hour, "d"))
		// Even a spec that names R-audit (a bug in a caller) cannot remove an audit row.
		rollAll(t, f)
		r := must(f.Store().Trim(ctx, "s", []retention.TrimSpec{
			{Class: retention.ClassAudit, Before: t0.Add(1000 * day)}, {Class: retention.ClassDebug, Before: t0.Add(1000 * day)}}, retention.Op{}))
		left := f.Rows("s")
		if byClass(left, a).Seq != a {
			t.Fatalf("the audit row was removed: %+v", r)
		}
		if f.Store().Caps().Trim == retention.TrimPerRow && byClass(left, d).Seq != 0 {
			t.Fatal("per-row: the debug row behind the audit row should go")
		}
		if marks := must(f.Store().Watermarks(ctx, "s")); marks[retention.ClassAudit] != 0 {
			t.Fatalf("audit watermark = %d, want none", marks[retention.ClassAudit])
		}
	}))

	t.Run("RowsNotPastEveryTierStay", guard(func(t *testing.T) {
		f := newFixture(t)
		old := add(t, f, row(retention.ClassDebug, 0, "old"))
		young := add(t, f, row(retention.ClassDebug, 5*day, "young"))
		rollAll(t, f)
		must(f.Store().Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}}, retention.Op{}))
		left := f.Rows("s")
		if byClass(left, old).Seq != 0 || byClass(left, young).Seq != young {
			t.Fatalf("rows left = %+v: only the row past the cutoff may go", left)
		}
	}))

	t.Run("NoRemovalWithoutACommittedCoveringRollup", guard(func(t *testing.T) {
		f := newFixture(t)
		a := add(t, f, row(retention.ClassActivity, 0, "a"))
		spec := retention.TrimSpec{Class: retention.ClassActivity, Before: t0.Add(30 * day), RequireRolled: true}
		if r := must(f.Store().Trim(ctx, "s", []retention.TrimSpec{spec}, retention.Op{})); r.Rows != 0 {
			t.Fatalf("removed an unrolled row: %+v", r)
		}
		must(f.Store().Rollup(ctx, "s", retention.RollupSpec{Class: retention.ClassActivity, Buckets: []retention.Duration{retention.D(day)}, ClosedBefore: t0.Add(10 * day)}, retention.Op{}))
		if r := must(f.Store().Trim(ctx, "s", []retention.TrimSpec{spec}, retention.Op{})); r.Rows != 1 || byClass(f.Rows("s"), a).Seq != 0 {
			t.Fatalf("after the rollup the row should go: %+v", r)
		}
	}))

	t.Run("PerRowRemovalGoesPerClassInCursorOrder", guard(func(t *testing.T) {
		f := newFixture(t)
		if f.Store().Caps().Trim != retention.TrimPerRow {
			t.Skip("store trims head-only")
		}
		var d, a []uint64
		for i := range 4 {
			d = append(d, add(t, f, row(retention.ClassDebug, time.Duration(2*i)*time.Hour, "d")))
			a = append(a, add(t, f, row(retention.ClassActivity, time.Duration(2*i+1)*time.Hour, "a")))
		}
		specs := []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}, {Class: retention.ClassActivity, Before: t0.Add(day)}}
		for step := 0; step < 6; step++ {
			rollAll(t, f)
			must(f.Store().Trim(ctx, "s", specs, retention.Op{Limit: 3}))
			left := f.Rows("s")
			// For each class what remains must be a suffix of its cursor order: no earlier row stays while a later one is gone.
			for class, seqs := range map[string][]uint64{retention.ClassDebug: d, retention.ClassActivity: a} {
				// A present row followed by a missing one is the violation.
				seenPresent := false
				for _, q := range seqs {
					present := byClass(left, q).Seq == q
					if seenPresent && !present {
						t.Fatalf("step %d: %s row %d was removed before an earlier row of its class: %+v", step, class, q, left)
					}
					seenPresent = seenPresent || present
				}
			}
			if len(left) == 0 {
				break
			}
		}
		if len(f.Rows("s")) != 0 {
			t.Fatalf("rows left after repeated bounded calls: %+v", f.Rows("s"))
		}
	}))

	t.Run("PerRowStopsAClassAtItsFirstRowThatMustStay", guard(func(t *testing.T) {
		f := newFixture(t)
		if f.Store().Caps().Trim != retention.TrimPerRow {
			t.Skip("store trims head-only")
		}
		e1 := add(t, f, row(retention.ClassActivity, 0, "e1"))
		young := add(t, f, row(retention.ClassActivity, 10*day, "young")) // inside its tier: stays
		e2 := add(t, f, row(retention.ClassActivity, time.Hour, "e2"))    // old enough, but behind a row that stays
		o := add(t, f, row(retention.ClassDebug, 2*time.Hour, "other class"))
		rollAll(t, f)
		must(f.Store().Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassActivity, Before: t0.Add(day)}, {Class: retention.ClassDebug, Before: t0.Add(day)}}, retention.Op{}))
		left := f.Rows("s")
		if byClass(left, e1).Seq != 0 || byClass(left, young).Seq != young || byClass(left, e2).Seq != e2 || byClass(left, o).Seq != 0 {
			t.Fatalf("rows left = %+v: e1 and the other class go; young stays and holds back e2 (cursor order)", left)
		}
	}))

	t.Run("WatermarkIsPerClassPersistedAndMonotonic", guard(func(t *testing.T) {
		f := newFixture(t)
		st := f.Store()
		d1 := add(t, f, row(retention.ClassDebug, 0, "d1"))
		a1 := add(t, f, row(retention.ClassActivity, time.Hour, "a1"))
		d2 := add(t, f, row(retention.ClassDebug, 2*time.Hour, "d2"))
		rollAll(t, f)
		must(st.Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(time.Hour)}}, retention.Op{}))
		marks := must(st.Watermarks(ctx, "s"))
		if st.Caps().Trim == retention.TrimHeadOnly {
			if len(marks) != 0 {
				t.Fatalf("a head-only store has no per-class watermarks: %v", marks)
			}
			return
		}
		if marks[retention.ClassDebug] != d1 || len(marks) != 1 {
			t.Fatalf("watermarks = %v, want only debug at %d", marks, d1)
		}
		must(st.Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}, {Class: retention.ClassActivity, Before: t0.Add(day)}}, retention.Op{}))
		marks = must(st.Watermarks(ctx, "s"))
		if marks[retention.ClassDebug] != d2 || marks[retention.ClassActivity] != a1 {
			t.Fatalf("watermarks = %v, want debug %d and activity %d", marks, d2, a1)
		}
		// Never backwards: a call that removes nothing, or older rows only, leaves them.
		must(st.Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0}}, retention.Op{}))
		if again := must(st.Watermarks(ctx, "s")); again[retention.ClassDebug] != d2 || again[retention.ClassActivity] != a1 {
			t.Fatalf("watermarks moved: %v", again)
		}
		// Cursors are never reused: a new row gets a cursor above every removed one.
		if n := add(t, f, row(retention.ClassDebug, 3*time.Hour, "n")); n <= d2 {
			t.Fatalf("new cursor %d reuses the range at or below %d", n, d2)
		}
	}))

	t.Run("WatermarkMovesAtomicallyWithTheRemoval", guard(func(t *testing.T) {
		f := newFixture(t)
		fx, ok := f.(Faulty)
		if f.Store().Caps().Trim != retention.TrimPerRow || !ok {
			t.Skip("needs a per-row store that can fail between the delete and the watermark")
		}
		add(t, f, row(retention.ClassDebug, 0, "d1"))
		add(t, f, row(retention.ClassDebug, time.Hour, "d2"))
		specs := []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}}
		boom := errors.New("injected")
		fx.InjectFault(FaultTrim, boom)
		rollAll(t, f)
		if _, err := f.Store().Trim(ctx, "s", specs, retention.Op{}); !errors.Is(err, boom) {
			t.Fatalf("Trim err = %v, want the injected fault", err)
		}
		if n := len(f.Rows("s")); n != 2 {
			t.Fatalf("%d rows left after a failed trim: the delete must roll back with the watermark", n)
		}
		if marks := must(f.Store().Watermarks(ctx, "s")); len(marks) != 0 {
			t.Fatalf("the watermark moved without the delete: %v", marks)
		}
		fx.InjectFault(FaultTrim, nil)
		if r := must(f.Store().Trim(ctx, "s", specs, retention.Op{})); r.Rows != 2 {
			t.Fatalf("replay = %+v", r)
		}
		if marks := must(f.Store().Watermarks(ctx, "s")); marks[retention.ClassDebug] != 2 {
			t.Fatalf("watermarks = %v", marks)
		}
	}))

	t.Run("RollupCountsAndFlagsMoveTogetherAndReplayDoesNotDoubleCount", guard(func(t *testing.T) {
		f := newFixture(t)
		fx, ok := f.(Faulty)
		if !ok {
			t.Skip("needs a store that can fail between the counts and the rolled flag")
		}
		add(t, f, row(retention.ClassState, 0, "a"))
		add(t, f, row(retention.ClassState, time.Minute, "b"))
		spec := retention.RollupSpec{Class: retention.ClassState, Buckets: []retention.Duration{retention.D(time.Hour)}, ClosedBefore: t0.Add(day)}
		boom := errors.New("injected")
		fx.InjectFault(FaultRollup, boom)
		if _, err := f.Store().Rollup(ctx, "s", spec, retention.Op{}); !errors.Is(err, boom) {
			t.Fatalf("Rollup err = %v", err)
		}
		if len(f.Buckets("s")) != 0 {
			t.Fatal("bucket counts survived a failed call")
		}
		for _, r := range f.Rows("s") {
			if r.Rolled {
				t.Fatal("a row was flagged rolled by a failed call")
			}
		}
		fx.InjectFault(FaultRollup, nil)
		must(f.Store().Rollup(ctx, "s", spec, retention.Op{}))
		must(f.Store().Rollup(ctx, "s", spec, retention.Op{})) // replay
		if bs := f.Buckets("s"); len(bs) != 1 || bs[0].Count != 2 {
			t.Fatalf("buckets = %+v, want one bucket counting 2 rows exactly once", bs)
		}
	}))

	t.Run("ReaderBelowAWatermarkSeesOneGapFrame", guard(func(t *testing.T) {
		f := newFixture(t)
		if f.Store().Caps().Trim != retention.TrimPerRow {
			t.Skip("store trims head-only")
		}
		d1 := add(t, f, row(retention.ClassDebug, 0, "d1"))
		a1 := add(t, f, row(retention.ClassActivity, time.Hour, "a1"))
		keep := add(t, f, row(retention.ClassActivity, 20*day, "keep"))
		d2 := add(t, f, row(retention.ClassDebug, 2*time.Hour, "d2"))
		rollAll(t, f)
		must(f.Store().Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}, {Class: retention.ClassActivity, Before: t0.Add(day)}}, retention.Op{}))
		marks := must(f.Store().Watermarks(ctx, "s"))
		// Starting from the beginning: one frame, naming the highest watermark; no cursor advance.
		g, ok := retention.ReadGap(marks, 0)
		if !ok || g.Reason != "retention" || g.ThroughCursor != d2 || g.Class != retention.ClassDebug {
			t.Fatalf("gap = %+v, %v; want retention through %d (debug)", g, ok, d2)
		}
		// Resuming from the cursor of a removed row is valid and still below the watermark: the same single frame.
		if g2, ok := retention.ReadGap(marks, d1); !ok || g2 != g {
			t.Fatalf("resume from a removed cursor: %+v, %v", g2, ok)
		}
		// At or past every watermark there is no frame.
		if _, ok := retention.ReadGap(marks, d2); ok {
			t.Fatal("a reader past every watermark got a gap frame")
		}
		_ = a1
		_ = keep
	}))

	// The largest-bucket rule is explicit: while the daily bucket is open, nothing
	// is counted hourly either. The buckets are a retention summary, not a live metric.
	t.Run("LargestBucketDelaysTheSmallerBucketCounts", guard(func(t *testing.T) {
		f := newFixture(t)
		add(t, f, row(retention.ClassState, 5*time.Minute, "a"))
		spec := retention.RollupSpec{Class: retention.ClassState, Buckets: []retention.Duration{retention.D(time.Hour), retention.D(day)}}
		spec.ClosedBefore = t0.Add(3 * time.Hour) // the hour is closed, the day is not
		if r := must(f.Store().Rollup(ctx, "s", spec, retention.Op{})); r.Rows != 0 || len(f.Buckets("s")) != 0 {
			t.Fatalf("Rollup = %+v, buckets %v: nothing may be counted, even hourly, while the daily bucket is open", r, f.Buckets("s"))
		}
		spec.ClosedBefore = t0.Add(day)
		if r := must(f.Store().Rollup(ctx, "s", spec, retention.Op{})); r.Rows != 1 || len(f.Buckets("s")) != 2 {
			t.Fatalf("after the day closed: %+v, buckets %v; want the row in both the hourly and the daily bucket", r, f.Buckets("s"))
		}
	}))

	// A change of the configured bucket set is forward only: a row already rolled
	// up is not counted again (no history for a new size, no double counting),
	// and rows rolled afterwards go into the new set.
	t.Run("BucketSetChangesApplyForwardOnly", guard(func(t *testing.T) {
		f := newFixture(t)
		add(t, f, row(retention.ClassState, 5*time.Minute, "old"))
		st := f.Store()
		hourly := retention.RollupSpec{Class: retention.ClassState, Buckets: []retention.Duration{retention.D(time.Hour)}, ClosedBefore: t0.Add(day)}
		must(st.Rollup(ctx, "s", hourly, retention.Op{}))
		add(t, f, row(retention.ClassState, 2*time.Hour, "new"))
		both := retention.RollupSpec{Class: retention.ClassState, Buckets: []retention.Duration{retention.D(time.Hour), retention.D(day)}, ClosedBefore: t0.Add(2 * day)}
		if r := must(st.Rollup(ctx, "s", both, retention.Op{})); r.Rows != 1 {
			t.Fatalf("Rollup after the change = %+v: only the new row is rolled", r)
		}
		var hours, days uint64
		for _, b := range f.Buckets("s") {
			if b.Size == retention.D(time.Hour) {
				hours += b.Count
			} else {
				days += b.Count
			}
		}
		if hours != 2 || days != 1 {
			t.Fatalf("hourly buckets count %d rows, daily %d; want 2 and 1 (the old row has no daily history, and nothing is counted twice)", hours, days)
		}
	}))

	t.Run("PerRowFailsClosedWithoutACoveringRollup", guard(func(t *testing.T) {
		f := newFixture(t)
		if f.Store().Caps().Trim != retention.TrimPerRow {
			t.Skip("store trims head-only")
		}
		a := add(t, f, row(retention.ClassDebug, 0, "d"))
		// A class with no rollups configured plans RequireRolled=false; a per-row store still removes nothing unrolled.
		r := must(f.Store().Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day), RequireRolled: false}}, retention.Op{}))
		if r.Rows != 0 || byClass(f.Rows("s"), a).Seq != a {
			t.Fatalf("a per-row store removed an unrolled row: %+v", r)
		}
		if marks := must(f.Store().Watermarks(ctx, "s")); len(marks) != 0 {
			t.Fatalf("watermarks moved without a removal: %v", marks)
		}
	}))

	t.Run("DryRunAndPruneHonourMaxDuration", guard(func(t *testing.T) {
		f := newFixture(t)
		if tb, ok := f.(TimeBounded); !ok || !tb.HonoursMaxDuration() {
			t.Skip("store bounds calls by Limit, not by time")
		}
		for i := range 30 {
			add(t, f, row(retention.ClassState, time.Duration(i)*day, "x"))
		}
		st := f.Store()
		r := must(st.Tombstone(ctx, "s", retention.TombstoneSpec{Class: retention.ClassState, Before: t0.Add(100 * day)}, retention.Op{DryRun: true, MaxDuration: time.Nanosecond}))
		if r.Rows < 1 || r.Rows >= 30 || !r.More {
			t.Fatalf("dry-run Tombstone with a 1ns budget = %+v: at least one row, then stop, More", r)
		}
		must(st.Rollup(ctx, "s", retention.RollupSpec{Class: retention.ClassState, Buckets: []retention.Duration{retention.D(day)}, ClosedBefore: t0.Add(200 * day)}, retention.Op{}))
		p := must(st.Prune(ctx, "s", retention.PruneSpec{Class: retention.ClassState, Bucket: retention.D(day), Before: t0.Add(200 * day)}, retention.Op{MaxDuration: time.Nanosecond}))
		if p.Rows < 1 || p.Rows >= 30 || !p.More {
			t.Fatalf("Prune with a 1ns budget = %+v", p)
		}
	}))

	t.Run("ConcurrentCallsCountEachRowOnce", guard(func(t *testing.T) {
		f := newFixture(t)
		for i := range 12 {
			add(t, f, row(retention.ClassState, time.Duration(i)*time.Minute, "x"))
		}
		st := f.Store()
		roll := retention.RollupSpec{Class: retention.ClassState, Buckets: []retention.Duration{retention.D(time.Hour)}, ClosedBefore: t0.Add(day)}
		tomb := retention.TombstoneSpec{Class: retention.ClassState, Before: t0.Add(day)}
		var wg sync.WaitGroup
		var rolled, tombed atomic.Int64
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, err := st.Rollup(ctx, "s", roll, retention.Op{Limit: 5})
				if err == nil {
					rolled.Add(r.Rows)
				}
				for {
					tr, err := st.Tombstone(ctx, "s", tomb, retention.Op{Limit: 5})
					if err != nil {
						return
					}
					tombed.Add(tr.Rows)
					if !tr.More {
						return
					}
				}
			}()
		}
		wg.Wait()
		// Finish whatever the racing calls left, then check totals: each row exactly once.
		for {
			r := must(st.Rollup(ctx, "s", roll, retention.Op{}))
			rolled.Add(r.Rows)
			if !r.More {
				break
			}
		}
		for {
			r := must(st.Tombstone(ctx, "s", tomb, retention.Op{}))
			tombed.Add(r.Rows)
			if !r.More {
				break
			}
		}
		if rolled.Load() != 12 || tombed.Load() != 12 {
			t.Fatalf("rolled %d, tombstoned %d; each of 12 rows must be counted exactly once across racing calls", rolled.Load(), tombed.Load())
		}
		var counted uint64
		for _, b := range f.Buckets("s") {
			counted += b.Count
		}
		if counted != 12 {
			t.Fatalf("buckets count %d rows, want 12", counted)
		}
	}))

	t.Run("RowsOnHoldAreNeverTouched", guard(func(t *testing.T) {
		f := newFixture(t)
		if hs, ok := f.(HoldSupport); !ok || !hs.SupportsHold() {
			t.Skip("fixture has no rows on hold")
		}
		st := f.Store()
		pend := row("", 0, "pending")
		pend.Hold = true
		hp := add(t, f, pend)
		held := row(retention.ClassDebug, 0, "held")
		held.Hold = true
		hh := add(t, f, held)
		after := add(t, f, row(retention.ClassDebug, time.Hour, "after"))
		must(st.Classify(ctx, "s", classOf, retention.Op{}))
		must(st.Rollup(ctx, "s", retention.RollupSpec{Class: retention.ClassDebug, Buckets: []retention.Duration{retention.D(time.Hour)}, ClosedBefore: t0.Add(day)}, retention.Op{}))
		must(st.Tombstone(ctx, "s", retention.TombstoneSpec{Class: retention.ClassDebug, Before: t0.Add(day)}, retention.Op{}))
		must(st.Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}}, retention.Op{}))
		left := f.Rows("s")
		for _, seq := range []uint64{hp, hh} {
			r := byClass(left, seq)
			if r.Seq != seq || r.Tombstoned || r.Rolled || string(r.Payload) == "" {
				t.Fatalf("held row %d was touched: %+v", seq, r)
			}
		}
		if byClass(left, hp).Class != "" {
			t.Fatal("a pending row was classified")
		}
		// Cursor order: the row after a held row of its class must not be removed either (head-only stops at it too).
		if byClass(left, after).Seq != after {
			t.Fatalf("a row behind a held row of its class was removed: %+v", left)
		}
	}))

	t.Run("OpsHonourACancelledContext", guard(func(t *testing.T) {
		f := newFixture(t)
		add(t, f, row(retention.ClassDebug, 0, "d"))
		st := f.Store()
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		checks := map[string]error{}
		_, checks["Streams"] = st.Streams(cctx)
		_, checks["Stats"] = st.Stats(cctx, "s")
		_, checks["Watermarks"] = st.Watermarks(cctx, "s")
		_, checks["Classify"] = st.Classify(cctx, "s", classOf, retention.Op{})
		_, checks["Rollup"] = st.Rollup(cctx, "s", retention.RollupSpec{Class: retention.ClassDebug, Buckets: []retention.Duration{retention.D(time.Hour)}, ClosedBefore: t0.Add(day)}, retention.Op{})
		_, checks["Tombstone"] = st.Tombstone(cctx, "s", retention.TombstoneSpec{Class: retention.ClassDebug, Before: t0.Add(day)}, retention.Op{})
		_, checks["Trim"] = st.Trim(cctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}}, retention.Op{})
		_, checks["Prune"] = st.Prune(cctx, "s", retention.PruneSpec{Class: retention.ClassDebug, Bucket: retention.D(time.Hour), Before: t0.Add(day)}, retention.Op{})
		for name, err := range checks {
			if err == nil {
				t.Errorf("%s ignored a cancelled context", name)
			}
		}
		if rows := f.Rows("s"); len(rows) != 1 || rows[0].Tombstoned {
			t.Fatalf("a cancelled call wrote: %+v", rows)
		}
	}))

	t.Run("RowByRowStoresHonourMaxDuration", guard(func(t *testing.T) {
		f := newFixture(t)
		if tb, ok := f.(TimeBounded); !ok || !tb.HonoursMaxDuration() {
			t.Skip("store bounds calls by Limit, not by time")
		}
		for range 20 {
			add(t, f, row(retention.ClassState, 0, "x"))
		}
		r := must(f.Store().Tombstone(ctx, "s", retention.TombstoneSpec{Class: retention.ClassState, Before: t0.Add(day)}, retention.Op{MaxDuration: time.Nanosecond}))
		if r.Rows < 1 || r.Rows >= 20 || !r.More {
			t.Fatalf("Tombstone with a 1ns budget = %+v: it must make progress on at least one row, stop, and say More", r)
		}
	}))

	t.Run("StreamsIncludesThoseWithOnlyRollups", guard(func(t *testing.T) {
		f := newFixture(t)
		add(t, f, row(retention.ClassDebug, 0, "a"))
		st := f.Store()
		must(st.Rollup(ctx, "s", retention.RollupSpec{Class: retention.ClassDebug, Buckets: []retention.Duration{retention.D(time.Hour)}, ClosedBefore: t0.Add(day)}, retention.Op{}))
		must(st.Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day), RequireRolled: true}}, retention.Op{}))
		if len(f.Rows("s")) != 0 {
			t.Fatal("setup: the row should be trimmed")
		}
		got := must(st.Streams(ctx))
		if len(got) != 1 || got[0] != "s" {
			t.Fatalf("Streams = %v; a stream with only rollup buckets left still needs pruning", got)
		}
	}))

	t.Run("UnknownStreamIsEmpty", guard(func(t *testing.T) {
		f := newFixture(t)
		st := f.Store()
		if r := must(st.Tombstone(ctx, "nope", retention.TombstoneSpec{Class: retention.ClassState, Before: t0}, retention.Op{})); r.Rows != 0 {
			t.Fatalf("Tombstone on an unknown stream = %+v", r)
		}
		if stats := must(st.Stats(ctx, "nope")); len(stats) != 0 {
			t.Fatalf("Stats = %+v", stats)
		}
	}))
}
