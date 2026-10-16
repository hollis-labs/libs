package retention

import (
	"context"
	"errors"
	"sort"
	"time"
)

// SecretClass is the key under which work on secret-sensitivity rows is
// reported: they belong to no class.
const SecretClass = "secret"

// Budget bounds a whole pass.
type Budget struct {
	// MaxRows is the most rows one pass may change (0 = unlimited).
	MaxRows int64
	// MaxElapsed is the longest one pass may run (0 = unlimited).
	MaxElapsed time.Duration
}

// Janitor applies a retention policy to a [Store]. It starts no goroutines
// and no timers and never vacuums: the caller schedules passes. Every pass is
// idempotent and safe to abandon, and the work is cut into short store calls
// (at most MaxTxRows rows and MaxTxDuration each) so a store with a single
// writer is released between them.
type Janitor struct {
	store   Store
	planner *Planner
	budget  Budget
	txRows  int
	txDur   time.Duration
	dryRun  bool
	elapsed func() time.Time
}

// Option configures a [Janitor].
type Option func(*Janitor)

// WithBudget bounds a whole pass.
func WithBudget(b Budget) Option { return func(j *Janitor) { j.budget = b } }

// WithMaxTxRows bounds the rows one store call may change (default 1000).
func WithMaxTxRows(n int) Option {
	return func(j *Janitor) {
		if n > 0 {
			j.txRows = n
		}
	}
}

// WithMaxTxDuration bounds how long one store call may run (default 100ms).
func WithMaxTxDuration(d time.Duration) Option {
	return func(j *Janitor) {
		if d > 0 {
			j.txDur = d
		}
	}
}

// WithDryRun makes every pass count what it would do and write nothing.
func WithDryRun(on bool) Option { return func(j *Janitor) { j.dryRun = on } }

// NewJanitor returns a Janitor over store, planning with planner.
func NewJanitor(store Store, planner *Planner, opts ...Option) *Janitor {
	j := &Janitor{store: store, planner: planner, txRows: 1000, txDur: 100 * time.Millisecond, elapsed: time.Now}
	for _, o := range opts {
		o(j)
	}
	return j
}

// ClassReport is one class's outcome in a pass.
type ClassReport struct {
	// Scanned is the rows the class had in the stream once the pass had
	// classified them (before it tombstoned or trimmed any).
	Scanned       int64
	Classified    int64
	Tombstoned    int64
	RolledUp      int64
	RollupBuckets int64
	Trimmed       int64
	RollupsPruned int64
	// BytesFreed is an estimate.
	BytesFreed int64
	// Backlog is rows still past their tombstone age after the pass (nonzero
	// when the pass stopped early or a store call failed).
	Backlog int64
	// OldestRemaining is the recorded time of the oldest row left; zero if none.
	OldestRemaining time.Time
	Errors          int
	FirstError      string
}

// Report is the outcome of one pass.
type Report struct {
	Classes map[string]*ClassReport
	Streams int
	// HeldByHead counts rows past their envelope age that a head-only trim
	// could not remove because an earlier row must stay.
	HeldByHead int64
	// StoppedEarly is true when the budget (rows or time) ended the pass.
	StoppedEarly bool
	// Resume, when StoppedEarly, is the token to pass to RunFrom. Starting over
	// with Run is equally correct; Resume only skips streams already finished.
	Resume  string
	Elapsed time.Duration
	DryRun  bool
	// Skipped is true when another janitor held the store lock.
	// (In a dry run nothing is classified, so rows that have no class yet are
	// invisible to the class-based counts; Classified says how many there are.)
	Skipped bool
	// Trim is the store's trim mode.
	Trim TrimMode
}

func (r *Report) class(name string) *ClassReport {
	if r.Classes == nil {
		r.Classes = map[string]*ClassReport{}
	}
	c := r.Classes[name]
	if c == nil {
		c = &ClassReport{}
		r.Classes[name] = c
	}
	return c
}

func (c *ClassReport) fail(err error) {
	c.Errors++
	if c.FirstError == "" {
		c.FirstError = err.Error()
	}
}

// Summary is the metadata-only view of a Report for events and health
// endpoints: counts and flags, no stream names, ids, policy ids or payloads.
type Summary struct {
	Streams      int                     `json:"streams"`
	DryRun       bool                    `json:"dry_run,omitempty"`
	StoppedEarly bool                    `json:"stopped_early,omitempty"`
	Skipped      bool                    `json:"skipped,omitempty"`
	HeldByHead   int64                   `json:"held_by_head,omitempty"`
	ElapsedMS    int64                   `json:"elapsed_ms"`
	Classes      map[string]ClassSummary `json:"classes"`
}

// ClassSummary is the counts of one class.
type ClassSummary struct {
	Scanned       int64 `json:"scanned"`
	Tombstoned    int64 `json:"tombstoned"`
	RolledUp      int64 `json:"rolled_up"`
	Trimmed       int64 `json:"trimmed"`
	RollupsPruned int64 `json:"rollups_pruned"`
	BytesFreed    int64 `json:"bytes_freed"`
	Backlog       int64 `json:"backlog"`
	Errors        int   `json:"errors"`
}

// Summary returns the metadata-only view.
func (r Report) Summary() Summary {
	s := Summary{Streams: r.Streams, DryRun: r.DryRun, StoppedEarly: r.StoppedEarly, Skipped: r.Skipped,
		HeldByHead: r.HeldByHead, ElapsedMS: r.Elapsed.Milliseconds(), Classes: map[string]ClassSummary{}}
	for name, c := range r.Classes {
		s.Classes[name] = ClassSummary{Scanned: c.Scanned, Tombstoned: c.Tombstoned, RolledUp: c.RolledUp, Trimmed: c.Trimmed,
			RollupsPruned: c.RollupsPruned, BytesFreed: c.BytesFreed, Backlog: c.Backlog, Errors: c.Errors}
	}
	return s
}

// pass is the state of one Run.
type pass struct {
	j       *Janitor
	rep     *Report
	start   time.Time
	rows    int64
	stopped bool
}

func (p *pass) over() bool {
	b := p.j.budget
	if (b.MaxRows > 0 && p.rows >= b.MaxRows) || (b.MaxElapsed > 0 && p.j.elapsed().Sub(p.start) >= b.MaxElapsed) {
		p.stopped = true
	}
	return p.stopped
}

func (p *pass) op() Op {
	op := Op{Limit: p.j.txRows, MaxDuration: p.j.txDur, DryRun: p.j.dryRun}
	if p.j.dryRun && p.j.budget.MaxElapsed > 0 {
		// A dry-run count is one statement over possibly millions of rows: it
		// gets what is left of the pass budget, not the per-transaction slice.
		op.MaxDuration = max(p.j.budget.MaxElapsed-p.j.elapsed().Sub(p.start), time.Millisecond)
	}
	return op
}

// Run does one pass over every stream. It returns a non-nil error only for
// failures that prevent the pass (listing streams, taking the lock, a
// cancelled context); a failing store call is counted in the class report and
// the pass goes on.
func (j *Janitor) Run(ctx context.Context, now time.Time) (Report, error) {
	return j.RunFrom(ctx, now, "")
}

// RunFrom is Run starting at the stream named by a Resume token ("" starts at the first).
func (j *Janitor) RunFrom(ctx context.Context, now time.Time, resume string) (Report, error) {
	rep := Report{DryRun: j.dryRun, Trim: j.store.Caps().Trim}
	t0 := j.elapsed()
	defer func() { rep.Elapsed = j.elapsed().Sub(t0) }()

	unlock, ok, err := j.store.TryLock(ctx)
	if err != nil {
		return rep, err
	}
	if !ok {
		rep.Skipped = true
		return rep, nil
	}
	defer unlock()

	streams, err := j.store.Streams(ctx)
	if err != nil {
		return rep, err
	}
	sort.Strings(streams)
	p := &pass{j: j, rep: &rep, start: t0}
	for _, stream := range streams {
		if resume != "" && stream < resume {
			continue
		}
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if p.over() {
			rep.StoppedEarly, rep.Resume = true, stream
			return rep, nil
		}
		rep.Streams++
		if err := p.stream(ctx, stream, now); err != nil {
			return rep, err
		}
		if p.stopped {
			rep.StoppedEarly, rep.Resume = true, stream
			break
		}
	}
	return rep, nil
}

// drain calls f until it reports no more work, the budget is spent, or ctx ends.
func (p *pass) drain(ctx context.Context, f func(Op) (rows int64, more bool, err error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, more, err := f(p.op())
		p.rows += rows
		if err != nil || !more || p.j.dryRun {
			return err
		}
		if p.over() {
			return nil
		}
	}
}

func (p *pass) stream(ctx context.Context, stream string, now time.Time) error {
	j, rep := p.j, p.rep
	st := j.store
	fail := func(class string, err error) {
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			rep.class(class).fail(err)
		}
	}

	plan := j.planner.Plan(now)

	// 1. classify rows that have no class yet.
	if classOf, ok := classifier(j.planner); ok {
		err := p.drain(ctx, func(op Op) (int64, bool, error) {
			r, err := st.Classify(ctx, stream, classOf, op)
			return r.Rows, r.More, err
		})
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		fail("", err)
	}
	if stats, err := st.Stats(ctx, stream); err != nil {
		fail("", err)
	} else {
		for _, s := range stats {
			rep.class(s.Class).Scanned += s.Rows
		}
	}
	// 2. roll up closed buckets, before anything leaves its tier.
	for _, s := range plan.Rollups {
		if p.over() {
			return nil
		}
		err := p.drain(ctx, func(op Op) (int64, bool, error) {
			r, err := st.Rollup(ctx, stream, s, op)
			c := rep.class(s.Class)
			c.RolledUp += r.Rows
			c.RollupBuckets += r.Buckets
			return r.Rows, r.More, err
		})
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		fail(s.Class, err)
	}
	// 3. tombstone payloads.
	for _, s := range plan.Tombstones {
		if p.over() {
			return nil
		}
		class := s.Class
		if s.Secret {
			class = SecretClass
		}
		err := p.drain(ctx, func(op Op) (int64, bool, error) {
			r, err := st.Tombstone(ctx, stream, s, op)
			c := rep.class(class)
			c.Tombstoned += r.Rows
			c.BytesFreed += r.BytesFreed
			return r.Rows, r.More, err
		})
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		fail(class, err)
	}
	// 4. trim, all specs in one call so a head-only store sees the whole prefix.
	if len(plan.Trims) > 0 && !p.over() {
		var held int64
		err := p.drain(ctx, func(op Op) (int64, bool, error) {
			r, err := st.Trim(ctx, stream, plan.Trims, op)
			for class, n := range r.ByClass {
				rep.class(class).Trimmed += n
			}
			if r.BytesFreed > 0 {
				if len(r.ByClass) == 1 {
					for class := range r.ByClass {
						rep.class(class).BytesFreed += r.BytesFreed
					}
				} else {
					rep.class("").BytesFreed += r.BytesFreed // a mixed trim cannot be split
				}
			}
			held = r.Held
			return r.Rows, r.More, err
		})
		rep.HeldByHead += held
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		fail("", err)
	}
	// 5. prune expired rollup buckets.
	for _, s := range plan.Prunes {
		if p.over() {
			return nil
		}
		err := p.drain(ctx, func(op Op) (int64, bool, error) {
			r, err := st.Prune(ctx, stream, s, op)
			rep.class(s.Class).RollupsPruned += r.Rows
			return r.Rows, r.More, err
		})
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		fail(s.Class, err)
	}

	// What is left: oldest remaining row and the backlog past tombstone age.
	if stats, err := st.Stats(ctx, stream); err == nil {
		for _, s := range stats {
			c := rep.class(s.Class)
			if c.OldestRemaining.IsZero() || (!s.Oldest.IsZero() && s.Oldest.Before(c.OldestRemaining)) {
				c.OldestRemaining = s.Oldest
			}
		}
	}
	if !j.dryRun {
		for _, s := range plan.Tombstones {
			if s.Secret {
				continue
			}
			if r, err := st.Tombstone(ctx, stream, s, Op{DryRun: true}); err == nil {
				rep.class(s.Class).Backlog += r.Rows
			}
		}
	}
	return ctx.Err()
}

func classifier(pl *Planner) (func(Attrs) string, bool) {
	if _, ok := pl.ClassOf(Attrs{}); !ok {
		return nil, false
	}
	return func(a Attrs) string { c, _ := pl.ClassOf(a); return c }, true
}
