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
	h := &hookStore{Store: m, fail: func(string) error { return errors.New("disk is on fire") }}
	rep, err := jan(t, h).Run(context.Background(), base.Add(400*day))
	if err != nil {
		t.Fatalf("a failing store call must not fail the pass: %v", err)
	}
	st := rep.Classes[retention.ClassState]
	if st.Errors != 1 || st.FirstError != "disk is on fire" || st.Tombstoned != 0 {
		t.Fatalf("state report = %+v", st)
	}
	if rep.Classes[retention.ClassActivity].Tombstoned != 1 {
		t.Fatalf("the activity class was not processed: %+v", rep.Classes[retention.ClassActivity])
	}
	if st.Backlog != 1 {
		t.Fatalf("state backlog = %d, want 1 (the row it could not tombstone)", st.Backlog)
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
