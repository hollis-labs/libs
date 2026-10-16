package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	t.Run("TombstoneRespectsLimitAndPolicyID", guard(func(t *testing.T) {
		f := newFixture(t)
		for range 5 {
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
		if byClass(f.Rows("s"), o).Tombstoned {
			t.Fatal("a row with another policy id was touched")
		}
		ids := must(st.PolicyIDs(ctx, "s"))
		if len(ids) != 2 || ids[0] != "" || ids[1] != "v1" {
			t.Fatalf("PolicyIDs = %v", ids)
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
			r = must(st.Trim(ctx, "s", []retention.TrimSpec{{Class: retention.ClassDebug, Before: t0.Add(day)}}, retention.Op{}))
			if r.Rows != 1 || byClass(f.Rows("s"), c).Seq != 0 {
				t.Fatalf("per-row trim: %+v", r)
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
