package retention_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention/storetest"
)

var (
	base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	day  = 24 * time.Hour
)

func jan(t *testing.T, st retention.Store, opts ...retention.Option) *retention.Janitor {
	t.Helper()
	p := mustResolve(t, retention.Config{})
	return retention.NewJanitor(st, retention.NewPlanner(p), opts...)
}

func seed(t *testing.T, m *storetest.Mem, r storetest.Row) uint64 {
	t.Helper()
	if r.Stream == "" {
		r.Stream = "env"
	}
	seq, err := m.Add(r)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func state(at time.Duration, payload string) storetest.Row {
	return storetest.Row{At: base.Add(at), Payload: []byte(payload), Attrs: retention.Attrs{Category: "state"},
		Dims: storetest.Dims{Kind: "task.state_changed", Project: "p1"}}
}

func seedMixed(t *testing.T, m *storetest.Mem) {
	t.Helper()
	// Unclassified on purpose: the first pass classifies existing rows.
	seedMixedClass(t, m, false)
}

func seedMixedClass(t *testing.T, m *storetest.Mem, classified bool) {
	t.Helper()
	c := func(class string) string {
		if classified {
			return class
		}
		return ""
	}
	seed(t, m, storetest.Row{Class: c(retention.ClassAudit), At: base, Payload: []byte("audit-old"), Attrs: retention.Attrs{Category: "audit"}})
	ancient := state(0, "state-ancient") // 400d old: past full (180d), inside envelope (2y)
	ancient.Class = c(retention.ClassState)
	seed(t, m, ancient)
	recent := state(300*day, "state-recent") // inside full
	recent.Class = c(retention.ClassState)
	seed(t, m, recent)
	seed(t, m, storetest.Row{Class: c(retention.ClassActivity), At: base.Add(10 * day), Payload: []byte("tool-old"), Attrs: retention.Attrs{Category: "tool"}})
	seed(t, m, storetest.Row{Class: c(retention.ClassActivity), At: base.Add(399 * day), Payload: []byte("tool-new"), Attrs: retention.Attrs{Category: "tool"}})
	pii := state(0, "personal")
	pii.Class = c(retention.ClassState)
	pii.Attrs.PII = true
	seed(t, m, pii)
}

func TestJanitorFullPassAndIdempotence(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	seedMixed(t, m)
	now := base.Add(400 * day)
	j := jan(t, m)

	rep, err := j.Run(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped || rep.StoppedEarly || rep.Streams != 1 {
		t.Fatalf("report = %+v", rep)
	}
	rows := map[string]storetest.RowState{}
	for _, r := range m.Rows("env") {
		rows[string(r.Payload)+"|"+r.PayloadDigest[:min(len(r.PayloadDigest), 7)]] = r
	}
	// Audit is untouched; the old state row is a tombstone; the recent one keeps its payload.
	byAt := func(class string, at time.Duration) storetest.RowState {
		for _, r := range m.Rows("env") {
			if r.Class == class && r.At.Equal(base.Add(at)) && !r.Attrs.PII {
				return r
			}
		}
		return storetest.RowState{}
	}
	if a := byAt(retention.ClassAudit, 0); a.Tombstoned || string(a.Payload) != "audit-old" {
		t.Fatalf("audit row = %+v: full retention is forever", a)
	}
	if s := byAt(retention.ClassState, 300*day); s.Tombstoned || string(s.Payload) != "state-recent" {
		t.Fatalf("recent state row = %+v", s)
	}
	if s := byAt(retention.ClassState, 0); !s.Tombstoned || s.Payload != nil || !strings.HasPrefix(s.PayloadDigest, "sha256:") {
		t.Fatalf("old state row = %+v", s)
	}
	if p := func() storetest.RowState {
		for _, r := range m.Rows("env") {
			if r.Attrs.PII {
				return r
			}
		}
		return storetest.RowState{}
	}(); !p.Tombstoned || p.PayloadDigest != "withheld" {
		t.Fatalf("pii row = %+v: capped at 30d and withheld", p)
	}
	if tl := byAt(retention.ClassActivity, 10*day); !tl.Tombstoned || tl.Class != retention.ClassActivity {
		t.Fatalf("old tool row = %+v (30d full)", tl)
	}
	if tn := byAt(retention.ClassActivity, 399*day); tn.Tombstoned {
		t.Fatalf("new tool row = %+v", tn)
	}
	st := rep.Classes[retention.ClassState]
	if st == nil || st.Tombstoned != 2 || st.Scanned != 3 || st.Backlog != 0 || st.Errors != 0 {
		t.Fatalf("state class report = %+v", st)
	}
	if rep.Classes[retention.ClassAudit].Classified != 0 || rep.Classes[retention.ClassAudit].Tombstoned != 0 {
		t.Fatalf("audit class report = %+v", rep.Classes[retention.ClassAudit])
	}
	// The first pass classified the rows that had no class.
	for _, r := range m.Rows("env") {
		if r.Class == "" {
			t.Fatalf("row %d is still unclassified", r.Seq)
		}
	}

	// A second pass changes nothing.
	again, err := j.Run(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	for class, c := range again.Classes {
		if c.Classified+c.Tombstoned+c.RolledUp+c.Trimmed+c.RollupsPruned != 0 {
			t.Fatalf("second pass changed %s: %+v", class, c)
		}
	}
}

func TestJanitorDryRunMatchesAndWritesNothing(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	seedMixedClass(t, m, true)
	now := base.Add(400 * day)
	before := m.Rows("env")
	dry, err := jan(t, m, retention.WithDryRun(true)).Run(context.Background(), now)
	if err != nil || !dry.DryRun {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	for i, r := range m.Rows("env") {
		if r.Tombstoned || r.Rolled || r.Class != before[i].Class {
			t.Fatalf("a dry run wrote: %+v", r)
		}
	}
	real, _ := jan(t, m).Run(context.Background(), now)
	if dry.Classes[retention.ClassState].Tombstoned != real.Classes[retention.ClassState].Tombstoned ||
		dry.Classes[retention.ClassActivity].Tombstoned != real.Classes[retention.ClassActivity].Tombstoned {
		t.Fatalf("dry run tombstoned %d/%d, the real pass %d/%d", dry.Classes[retention.ClassState].Tombstoned,
			dry.Classes[retention.ClassActivity].Tombstoned, real.Classes[retention.ClassState].Tombstoned, real.Classes[retention.ClassActivity].Tombstoned)
	}
}

func TestJanitorRollsUpBeforeTrimAndTrimsHeadOnly(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	// Debug rows (7d full, 7d envelope, 1h rollups) with an audit row between them.
	dbg := func(at time.Duration) storetest.Row {
		return storetest.Row{At: base.Add(at), Payload: []byte("d"), Attrs: retention.Attrs{Category: "tool", Severity: "debug"}, Dims: storetest.Dims{Kind: "tool.call"}}
	}
	d1 := seed(t, m, dbg(time.Hour))
	d2 := seed(t, m, dbg(2*time.Hour))
	a := seed(t, m, storetest.Row{At: base.Add(3 * time.Hour), Payload: []byte("audit"), Attrs: retention.Attrs{Category: "audit"}})
	d3 := seed(t, m, dbg(4*time.Hour))
	now := base.Add(20 * day)

	rep, err := jan(t, m).Run(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	left := m.Rows("env")
	if len(left) != 2 || left[0].Seq != a || left[1].Seq != d3 {
		t.Fatalf("rows left = %+v, want only the audit row and the debug row behind it (d1=%d d2=%d)", left, d1, d2)
	}
	if m.PrunedThrough("env") != d2 {
		t.Fatalf("pruned through %d, want %d", m.PrunedThrough("env"), d2)
	}
	if rep.HeldByHead != 1 || rep.Classes[retention.ClassDebug].Trimmed != 2 {
		t.Fatalf("held %d, debug trimmed %d; want 1 held behind the audit row and 2 trimmed", rep.HeldByHead, rep.Classes[retention.ClassDebug].Trimmed)
	}
	// Rolled up before they left: the hourly buckets for the trimmed rows exist.
	var counted uint64
	for _, b := range m.Buckets("env") {
		if b.Class == retention.ClassDebug {
			counted += b.Count
		}
	}
	if counted != 3 {
		t.Fatalf("debug rollups count %d rows, want all 3 (the held row is rolled up too)", counted)
	}
	// The debug row held behind the audit row is tombstoned but still there.
	if !left[1].Tombstoned || !left[1].Rolled {
		t.Fatalf("held row = %+v", left[1])
	}

	// Per-row stores remove it as well.
	m2 := storetest.NewMem(retention.TrimPerRow)
	seed(t, m2, dbg(time.Hour))
	a2 := seed(t, m2, storetest.Row{At: base.Add(3 * time.Hour), Payload: []byte("audit"), Attrs: retention.Attrs{Category: "audit"}})
	seed(t, m2, dbg(4*time.Hour))
	rep2, err := jan(t, m2).Run(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if l := m2.Rows("env"); len(l) != 1 || l[0].Seq != a2 || rep2.HeldByHead != 0 || rep2.Trim != retention.TrimPerRow {
		t.Fatalf("per-row result = %+v, held %d", l, rep2.HeldByHead)
	}
}

func TestJanitorBudgetStopsEarlyAndResumes(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	for _, stream := range []string{"a", "b", "c"} {
		for i := range 6 {
			r := state(time.Duration(i)*time.Minute, "x")
			r.Stream = stream
			seed(t, m, r)
		}
	}
	now := base.Add(400 * day)
	// One-shot reference.
	ref := storetest.NewMem(retention.TrimHeadOnly)
	for _, stream := range []string{"a", "b", "c"} {
		for i := range 6 {
			r := state(time.Duration(i)*time.Minute, "x")
			r.Stream = stream
			seed(t, ref, r)
		}
	}
	want, _ := jan(t, ref).Run(context.Background(), now)

	j := jan(t, m, retention.WithBudget(retention.Budget{MaxRows: 20}), retention.WithMaxTxRows(5))
	var tomb int64
	resume := ""
	for pass := 0; ; pass++ {
		rep, err := j.RunFrom(context.Background(), now, resume)
		if err != nil {
			t.Fatal(err)
		}
		tomb += rep.Classes[retention.ClassState].Tombstoned
		if !rep.StoppedEarly {
			break
		}
		if rep.Resume == "" || pass > 20 {
			t.Fatalf("stopped early without a usable resume token: %+v", rep)
		}
		resume = rep.Resume
	}
	// A resumed series may revisit a stream that was cut mid-way, but never double-counts a tombstone.
	if tomb != want.Classes[retention.ClassState].Tombstoned {
		t.Fatalf("tombstoned %d over the resumed passes, want %d", tomb, want.Classes[retention.ClassState].Tombstoned)
	}
}

type hookStore struct {
	retention.Store
	mu     sync.Mutex
	limits []int
	fail   func(op string) error
}

func (h *hookStore) rec(op retention.Op) {
	h.mu.Lock()
	h.limits = append(h.limits, op.Limit)
	h.mu.Unlock()
}

func (h *hookStore) Tombstone(ctx context.Context, s string, sp retention.TombstoneSpec, op retention.Op) (retention.Result, error) {
	if !op.DryRun {
		h.rec(op)
	}
	if h.fail != nil && sp.Class == retention.ClassState && !sp.PII && !sp.Secret && !op.DryRun {
		if err := h.fail("tombstone"); err != nil {
			return retention.Result{}, err
		}
	}
	return h.Store.Tombstone(ctx, s, sp, op)
}

func TestJanitorUsesTheTxBoundsItWasGiven(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	for range 7 {
		seed(t, m, state(0, "x"))
	}
	h := &hookStore{Store: m}
	rep, err := jan(t, h, retention.WithMaxTxRows(3), retention.WithMaxTxDuration(40*time.Millisecond)).Run(context.Background(), base.Add(400*day))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.limits) < 3 {
		t.Fatalf("store calls = %v: 7 rows in batches of 3 need at least 3 tombstone calls", h.limits)
	}
	for _, l := range h.limits {
		if l != 3 {
			t.Fatalf("Op.Limit = %d, want 3", l)
		}
	}
	if rep.Classes[retention.ClassState].Tombstoned != 7 {
		t.Fatalf("tombstoned %d", rep.Classes[retention.ClassState].Tombstoned)
	}
}

func TestJanitorErrorsAreCountedAndThePassContinues(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	seed(t, m, state(0, "x"))
	seed(t, m, storetest.Row{At: base, Payload: []byte("t"), Attrs: retention.Attrs{Category: "tool"}})
	h := &hookStore{Store: m, fail: func(string) error { return errors.New("disk is on fire: payload secret-token-123") }}
	var raw []string
	rep, err := jan(t, h, retention.WithErrorLog(func(stream, class string, err error) { raw = append(raw, stream+"|"+class+"|"+err.Error()) })).Run(context.Background(), base.Add(400*day))
	if err != nil {
		t.Fatalf("a failing store call must not fail the pass: %v", err)
	}
	st := rep.Classes[retention.ClassState]
	if st.Errors != 1 || st.FirstError != "store_error" || st.Tombstoned != 0 {
		t.Fatalf("state report = %+v", st)
	}
	if rep.Classes[retention.ClassActivity].Tombstoned != 1 {
		t.Fatalf("the activity class was not processed: %+v", rep.Classes[retention.ClassActivity])
	}
	if st.Backlog != 1 {
		t.Fatalf("state backlog = %d, want 1 (the row it could not tombstone)", st.Backlog)
	}
	// The report never echoes the store's message (it can carry row content); the caller's own log does.
	if b, _ := json.Marshal(rep.Summary()); strings.Contains(string(b), "secret-token") || strings.Contains(st.FirstError, "disk") {
		t.Fatalf("a store message reached the report: %s / %q", b, st.FirstError)
	}
	if len(raw) != 1 || raw[0] != "env|store_error|disk is on fire: payload secret-token-123" {
		t.Fatalf("error log = %v", raw)
	}
}

func TestJanitorSkipsWhenLocked(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	seed(t, m, state(0, "x"))
	unlock, ok, _ := m.TryLock(context.Background())
	if !ok {
		t.Fatal("could not take the lock")
	}
	rep, err := jan(t, m).Run(context.Background(), base.Add(400*day))
	if err != nil || !rep.Skipped || len(rep.Classes) != 0 {
		t.Fatalf("report = %+v, %v; want Skipped and no work", rep, err)
	}
	unlock()
	if rep, _ = jan(t, m).Run(context.Background(), base.Add(400*day)); rep.Skipped {
		t.Fatal("still skipped after unlock")
	}
}

// D7 rev 2.2: the policy id a row was admitted under is audit only; the current
// configuration governs it.
func TestJanitorAppliesTheCurrentConfigToRowsAdmittedUnderAnotherPolicy(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	old := state(0, "y")
	old.PolicyID = "v0"
	u := seed(t, m, old)
	if _, err := jan(t, m).Run(context.Background(), base.Add(400*day)); err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Rows("env") {
		if r.Seq == u && !r.Tombstoned {
			t.Fatalf("a row admitted under another policy id was shielded: %+v", r)
		}
	}
}

func TestJanitorStopsOnCancelledContext(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	seed(t, m, state(0, "x"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := jan(t, m).Run(ctx, base.Add(400*day)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestSummaryIsMetadataOnly(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	r := state(0, "secret-looking payload")
	r.Stream = "env-with-a-name"
	r.PolicyID = ""
	seed(t, m, r)
	rep, _ := jan(t, m).Run(context.Background(), base.Add(400*day))
	b, err := json.Marshal(rep.Summary())
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"env-with-a-name", "payload", "p1", "task.state_changed"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("summary leaks %q: %s", leak, b)
		}
	}
	if !strings.Contains(string(b), `"tombstoned":1`) {
		t.Fatalf("summary = %s", b)
	}
}

func TestPrunesExpiredRollups(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	// A debug row from long ago, rolled up on an earlier pass.
	seed(t, m, storetest.Row{At: base, Payload: []byte("d"), Attrs: retention.Attrs{Category: "tool", Severity: "debug"}})
	j := jan(t, m)
	if _, err := j.Run(context.Background(), base.Add(10*day)); err != nil {
		t.Fatal(err)
	}
	if len(m.Buckets("env")) == 0 {
		t.Fatal("no rollup was built")
	}
	// The debug hourly rollup is kept 90d.
	rep, err := j.Run(context.Background(), base.Add(200*day))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Buckets("env")) != 0 || rep.Classes[retention.ClassDebug].RollupsPruned == 0 {
		t.Fatalf("buckets %d, report %+v", len(m.Buckets("env")), rep.Classes[retention.ClassDebug])
	}
}

// A dry-run count gets the rest of the pass budget as its MaxDuration, so a
// huge table cannot make a dry run unbounded.
func TestDryRunCountsAreBoundedByTheElapsedBudget(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	seed(t, m, state(0, "x"))
	var got []time.Duration
	h := &durStore{Store: m, got: &got}
	_, err := jan(t, h, retention.WithDryRun(true), retention.WithBudget(retention.Budget{MaxElapsed: 5 * time.Second})).Run(context.Background(), base.Add(400*day))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no dry-run call seen")
	}
	for _, d := range got {
		if d <= 0 || d > 5*time.Second {
			t.Fatalf("dry-run MaxDuration = %v, want within (0, 5s]", d)
		}
	}
	// And a normal pass keeps the per-transaction bound.
	got = nil
	if _, err := jan(t, h, retention.WithMaxTxDuration(40*time.Millisecond)).Run(context.Background(), base.Add(400*day)); err != nil {
		t.Fatal(err)
	}
	for _, d := range got {
		if d != 40*time.Millisecond {
			t.Fatalf("write-call MaxDuration = %v, want 40ms", d)
		}
	}
}

type durStore struct {
	retention.Store
	got *[]time.Duration
}

func (d *durStore) Tombstone(ctx context.Context, s string, sp retention.TombstoneSpec, op retention.Op) (retention.Result, error) {
	if !(op.DryRun && op.Limit == 0) { // ignore the report's own backlog counts
		*d.got = append(*d.got, op.MaxDuration)
	}
	return d.Store.Tombstone(ctx, s, sp, op)
}

type limitStore struct {
	retention.Store
	mu         sync.Mutex
	limits     []int
	rollLimits []int
	delay      time.Duration
}

func (l *limitStore) Tombstone(ctx context.Context, s string, sp retention.TombstoneSpec, op retention.Op) (retention.Result, error) {
	if !op.DryRun {
		l.mu.Lock()
		l.limits = append(l.limits, op.Limit)
		l.mu.Unlock()
		time.Sleep(l.delay)
	}
	return l.Store.Tombstone(ctx, s, sp, op)
}

func (l *limitStore) Rollup(ctx context.Context, s string, sp retention.RollupSpec, op retention.Op) (retention.RollupResult, error) {
	if !op.DryRun {
		l.mu.Lock()
		l.rollLimits = append(l.rollLimits, op.Limit)
		l.mu.Unlock()
	}
	return l.Store.Rollup(ctx, s, sp, op)
}

// A call is capped by what is left of the pass budget, never by the slice alone.
func TestPassBudgetCapsEachStoreCall(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	for range 10 {
		r := state(0, "x")
		r.Class = retention.ClassState // classified, so the row budget goes to the tombstones
		seed(t, m, r)
	}
	h := &limitStore{Store: m}
	rep, err := jan(t, h, retention.WithMaxTxRows(4), retention.WithBudget(retention.Budget{MaxRows: 6})).Run(context.Background(), base.Add(400*day))
	if err != nil {
		t.Fatal(err)
	}
	st := rep.Classes[retention.ClassState]
	if st.RolledUp+st.Tombstoned != 6 || !rep.StoppedEarly {
		t.Fatalf("changed %d rows (stopped early %v): a MaxRows budget of 6 must not be exceeded", st.RolledUp+st.Tombstoned, rep.StoppedEarly)
	}
	// One call per class spec (the empty audit class first), then 4 rows, then only the 2 that are left.
	if n := len(h.rollLimits); n < 2 || h.rollLimits[n-2] != 4 || h.rollLimits[n-1] != 2 {
		t.Fatalf("rollup call limits = %v, want the last two to be [4 2]: a call may only use what is left", h.rollLimits)
	}
	if len(h.limits) != 0 {
		t.Fatalf("tombstone calls %v after the budget was spent", h.limits)
	}
}

func TestReportCarriesElapsedPolicyAndClassified(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	seedMixed(t, m)
	h := &limitStore{Store: m, delay: 5 * time.Millisecond}
	rep, err := jan(t, h, retention.WithPolicyID("cfg-7")).Run(context.Background(), base.Add(400*day))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Elapsed < 5*time.Millisecond {
		t.Fatalf("Elapsed = %v: the deferred value must reach the caller", rep.Elapsed)
	}
	if rep.PolicyID != "cfg-7" || rep.Summary().PolicyID != "cfg-7" {
		t.Fatalf("policy id = %q / %q", rep.PolicyID, rep.Summary().PolicyID)
	}
	if rep.Classified != 6 {
		t.Fatalf("Classified = %d, want the 6 rows that had no class", rep.Classified)
	}
}

// When the time budget is spent before the end-of-pass reads, backlog and
// oldest are marked unknown (Partial), not left looking like zero.
func TestSpentBudgetMarksTheTailReadsPartial(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	seedMixedClass(t, m, true)
	h := &limitStore{Store: m, delay: 30 * time.Millisecond}
	rep, err := jan(t, h, retention.WithBudget(retention.Budget{MaxElapsed: 10 * time.Millisecond})).Run(context.Background(), base.Add(400*day))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.StoppedEarly {
		t.Fatal("the pass should have stopped early")
	}
	for name, c := range rep.Classes {
		if name == retention.SecretClass {
			continue
		}
		if !c.Partial {
			t.Fatalf("class %s: Partial = false after the budget ran out; its backlog %d would read as 'none'", name, c.Backlog)
		}
	}
}

func TestWatermarksReachTheReportAndSummary(t *testing.T) {
	m := storetest.NewMem(retention.TrimPerRow)
	a := seed(t, m, storetest.Row{At: base.Add(time.Hour), Payload: []byte("d"), Attrs: retention.Attrs{Category: "tool", Severity: "debug"}})
	seed(t, m, storetest.Row{At: base.Add(2 * time.Hour), Payload: []byte("audit"), Attrs: retention.Attrs{Category: "audit"}})
	b := seed(t, m, storetest.Row{At: base.Add(3 * time.Hour), Payload: []byte("d"), Attrs: retention.Attrs{Category: "tool", Severity: "debug"}})
	rep, err := jan(t, m, retention.WithPolicyID("cfg-7")).Run(context.Background(), base.Add(20*day))
	if err != nil {
		t.Fatal(err)
	}
	dbg := rep.Classes[retention.ClassDebug]
	if dbg.Watermark != b || dbg.Trimmed != 2 || rep.HeldByHead != 0 || a == b {
		t.Fatalf("debug = %+v, held %d: per-row removes both debug rows around the audit row, W_C = %d", dbg, rep.HeldByHead, b)
	}
	sum := rep.Summary()
	if sum.Classes[retention.ClassDebug].Watermark != b || sum.PolicyID != "cfg-7" {
		t.Fatalf("summary = %+v", sum)
	}
	if rep.Classes[retention.ClassAudit].Watermark != 0 {
		t.Fatal("audit has a watermark")
	}
}

func TestPassEventOnlyForPassesThatChangedSomething(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	seed(t, m, state(0, "x"))
	j := jan(t, m, retention.WithPolicyID("cfg-7"))
	now := base.Add(400 * day)
	rep, _ := j.Run(context.Background(), now)
	ev, ok := rep.PassEvent()
	if !ok || ev.Kind != "retention.pass" || ev.Category != "audit" || ev.Visibility != "private" || ev.Actor != "janitor" || ev.Summary.PolicyID != "cfg-7" {
		t.Fatalf("event = %+v, %v", ev, ok)
	}
	if again, _ := j.Run(context.Background(), now); func() bool { _, ok := again.PassEvent(); return ok }() {
		t.Fatal("a pass that changed nothing produced an event")
	}
	dry, _ := jan(t, storetest.NewMem(retention.TrimHeadOnly), retention.WithDryRun(true)).Run(context.Background(), now)
	if _, ok := dry.PassEvent(); ok {
		t.Fatal("a dry run produced an event")
	}
	unlock, _, _ := m.TryLock(context.Background())
	skipped, _ := j.Run(context.Background(), now)
	unlock()
	if _, ok := skipped.PassEvent(); ok {
		t.Fatal("a skipped pass produced an event")
	}
}

func TestReadGapPicksTheHighestWatermarkOnce(t *testing.T) {
	marks := map[string]uint64{retention.ClassDebug: 40, retention.ClassActivity: 90, retention.ClassState: 90}
	g, ok := retention.ReadGap(marks, 10)
	if !ok || g.Reason != "retention" || g.ThroughCursor != 90 || g.Class != retention.ClassActivity {
		t.Fatalf("gap = %+v, %v; ties go to the class that sorts first", g, ok)
	}
	if _, ok := retention.ReadGap(marks, 90); ok {
		t.Fatal("a reader at the highest watermark got a frame")
	}
	if g, ok := retention.ReadGap(marks, 50); !ok || g.ThroughCursor != 90 {
		t.Fatalf("gap = %+v", g)
	}
	if _, ok := retention.ReadGap(nil, 0); ok {
		t.Fatal("no watermarks, no frame")
	}
}

// A Resume token is bound to the effective policy id: after a configuration
// change the pass starts over instead of skipping streams the new config has not seen.
func TestResumeTokenIsBoundToThePolicyID(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	for _, stream := range []string{"a", "b", "c"} {
		r := state(0, "x")
		r.Stream = stream
		r.Class = retention.ClassState
		seed(t, m, r)
	}
	now := base.Add(400 * day)
	first, err := jan(t, m, retention.WithPolicyID("cfg-1"), retention.WithBudget(retention.Budget{MaxRows: 3}), retention.WithMaxTxRows(1)).Run(context.Background(), now)
	if err != nil || !first.StoppedEarly || first.Resume == "" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	// Same policy id: continues. Different id: starts over (stream "a" is visited again).
	same, _ := jan(t, m, retention.WithPolicyID("cfg-1"), retention.WithBudget(retention.Budget{MaxRows: 100})).RunFrom(context.Background(), now, first.Resume)
	other, _ := jan(t, m, retention.WithPolicyID("cfg-2"), retention.WithBudget(retention.Budget{MaxRows: 100})).RunFrom(context.Background(), now, first.Resume)
	if other.Streams != 3 || same.Streams >= other.Streams {
		t.Fatalf("streams visited: same-policy resume %d, other-policy resume %d; the latter must start over (3)", same.Streams, other.Streams)
	}
}

// D7 rev 2.2: the current configuration decides each pass. Shortening reaches
// rows that already exist; lengthening again restores nothing.
func TestCurrentConfigGovernsEachPass(t *testing.T) {
	m := storetest.NewMem(retention.TrimHeadOnly)
	r := storetest.Row{Class: retention.ClassActivity, At: base.Add(100 * day), Payload: []byte("tool"), Attrs: retention.Attrs{Category: "tool"}}
	id := seed(t, m, r)
	now := base.Add(110 * day) // the row is 10 days old
	run := func(c retention.Config) {
		t.Helper()
		p := mustResolve(t, c)
		if _, err := retention.NewJanitor(m, retention.NewPlanner(p)).Run(context.Background(), now); err != nil {
			t.Fatal(err)
		}
	}
	tombstoned := func() bool {
		for _, row := range m.Rows("env") {
			if row.Seq == id {
				return row.Tombstoned
			}
		}
		return false
	}
	run(retention.Config{}) // defaults: activity keeps whole rows 30 days
	if tombstoned() {
		t.Fatal("tombstoned inside its 30d full age")
	}
	run(retention.Config{Classes: map[string]retention.ClassPolicy{retention.ClassActivity: {Full: retention.D(5 * day), Envelope: retention.D(180 * day)}}})
	if !tombstoned() {
		t.Fatal("shortening the age did not reach the existing row")
	}
	run(retention.Config{}) // back to 30 days
	if !tombstoned() {
		t.Fatal("lengthening the age restored a tombstoned payload")
	}
}

type oddStore struct {
	retention.Store
	mode retention.TrimMode
}

func (o oddStore) Caps() retention.Caps { return retention.Caps{Trim: o.mode} }

// An unsupported or disallowed trim strategy fails explicitly and touches nothing.
func TestUnsupportedTrimStrategyFailsExplicitly(t *testing.T) {
	m := storetest.NewMem(retention.TrimPerRow)
	seed(t, m, state(0, "x"))
	for name, j := range map[string]*retention.Janitor{
		"unknown mode":                 jan(t, oddStore{Store: m, mode: retention.TrimMode(99)}),
		"per-row store, head-only job": jan(t, m, retention.WithTrimModes(retention.TrimHeadOnly)),
	} {
		rep, err := j.Run(context.Background(), base.Add(400*day))
		if !errors.Is(err, retention.ErrUnsupportedTrim) || rep.Streams != 0 {
			t.Fatalf("%s: err = %v, streams %d; want ErrUnsupportedTrim and no work", name, err, rep.Streams)
		}
		for _, r := range m.Rows("env") {
			if r.Tombstoned || r.Rolled {
				t.Fatalf("%s: a refused pass changed a row: %+v", name, r)
			}
		}
	}
	if _, err := jan(t, m, retention.WithTrimModes(retention.TrimPerRow)).Run(context.Background(), base.Add(400*day)); err != nil {
		t.Fatalf("allowed mode refused: %v", err)
	}
}
