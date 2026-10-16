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
	// MaxElapsed is the longest one pass may run (0 = unlimited). It also
	// bounds the dry-run counts and the end-of-pass reads (stats, backlog).
	MaxElapsed time.Duration
}

// Janitor applies a retention policy to a [Store]. It starts no goroutines
// and no timers and never vacuums: the caller schedules passes. Every pass is
// idempotent and safe to abandon, and the work is cut into short store calls
// (at most MaxTxRows rows and MaxTxDuration each, and never more than what is
// left of the pass Budget) so a store with a single writer is released between
// them.
type Janitor struct {
	store    Store
	planner  *Planner
	budget   Budget
	txRows   int
	txDur    time.Duration
	dryRun   bool
	policyID string
	elapsed  func() time.Time
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

// WithPolicyID names the effective policy (the configuration revision the
// pass runs under). It is reported in the Report and the pass event; it plays
// no part in what the pass does.
func WithPolicyID(id string) Option { return func(j *Janitor) { j.policyID = id } }

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
	// BytesFreed is an estimate. It makes no claim about file size.
	BytesFreed int64
	// Backlog is rows still past their tombstone age after the pass (nonzero
	// when the pass stopped early or a store call failed). Valid only when
	// Partial is false.
	Backlog int64
	// OldestRemaining is the recorded time of the oldest row left; zero if
	// none. Valid only when Partial is false.
	OldestRemaining time.Time
	// Watermark is the highest per-class watermark W_C across streams (a
	// TrimPerRow store; zero otherwise): every row of the class at or below it
	// was removed.
	Watermark uint64
	// Partial is true when Backlog and OldestRemaining could not be collected
	// in full (the pass ran out of budget): treat them as unknown, not as zero.
	Partial    bool
	Errors     int
	FirstError string
}

// Report is the outcome of one pass.
type Report struct {
	Classes map[string]*ClassReport
	Streams int
	// Classified is the rows given a class by this pass. In a dry run it is
	// the rows that would be: class-based counts for them are not in the
	// report, because a dry run cannot classify.
	Classified int64
	// HeldByHead counts rows past their envelope age that a head-only trim
	// could not remove because an earlier row must stay. Always zero for a
	// TrimPerRow store.
	HeldByHead int64
	// PolicyID is the effective policy named with [WithPolicyID].
	PolicyID string
	// StoppedEarly is true when the budget (rows or time) ended the pass.
	StoppedEarly bool
	// Resume, when StoppedEarly, is the token to pass to RunFrom. Starting over
	// with Run is equally correct; Resume only skips streams already finished.
	Resume  string
	Elapsed time.Duration
	DryRun  bool
	// Skipped is true when another janitor held the store lock.
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

// Changed reports whether the pass changed anything (a dry run changes
// nothing).
func (r Report) Changed() bool {
	if r.DryRun || r.Skipped {
		return false
	}
	if r.Classified > 0 {
		return true
	}
	for _, c := range r.Classes {
		if c.Tombstoned+c.RolledUp+c.Trimmed+c.RollupsPruned > 0 {
			return true
		}
	}
	return false
}

// Summary is the metadata-only view of a Report for events and health
// endpoints: counts, the effective policy id and per-class watermarks. It
// carries no stream names, row ids or payloads.
type Summary struct {
	PolicyID     string                  `json:"policy_id,omitempty"`
	Streams      int                     `json:"streams"`
	Classified   int64                   `json:"classified,omitempty"`
	DryRun       bool                    `json:"dry_run,omitempty"`
	StoppedEarly bool                    `json:"stopped_early,omitempty"`
	Skipped      bool                    `json:"skipped,omitempty"`
	HeldByHead   int64                   `json:"held_by_head,omitempty"`
	ElapsedMS    int64                   `json:"elapsed_ms"`
	Classes      map[string]ClassSummary `json:"classes"`
}

// ClassSummary is the counts of one class.
type ClassSummary struct {
	Scanned       int64  `json:"scanned"`
	Tombstoned    int64  `json:"tombstoned"`
	RolledUp      int64  `json:"rolled_up"`
	Trimmed       int64  `json:"trimmed"`
	RollupsPruned int64  `json:"rollups_pruned"`
	BytesFreed    int64  `json:"bytes_freed"`
	Backlog       int64  `json:"backlog"`
	Watermark     uint64 `json:"watermark,omitempty"`
	Partial       bool   `json:"partial,omitempty"`
	Errors        int    `json:"errors"`
}

// Summary returns the metadata-only view.
func (r Report) Summary() Summary {
	s := Summary{PolicyID: r.PolicyID, Streams: r.Streams, Classified: r.Classified, DryRun: r.DryRun, StoppedEarly: r.StoppedEarly,
		Skipped: r.Skipped, HeldByHead: r.HeldByHead, ElapsedMS: r.Elapsed.Milliseconds(), Classes: map[string]ClassSummary{}}
	for name, c := range r.Classes {
		s.Classes[name] = ClassSummary{Scanned: c.Scanned, Tombstoned: c.Tombstoned, RolledUp: c.RolledUp, Trimmed: c.Trimmed,
			RollupsPruned: c.RollupsPruned, BytesFreed: c.BytesFreed, Backlog: c.Backlog, Watermark: c.Watermark, Partial: c.Partial, Errors: c.Errors}
	}
	return s
}

// PassEvent is the metadata-only record a hub emits for a pass that changed
// something (D7 rev 2.3): category audit, visibility private, actor the
// janitor service, plus the per-class counts, watermarks, bytes estimate,
// effective policy id, stopped_early and errors in Summary.
type PassEvent struct {
	Kind       string
	Category   string
	Visibility string
	Actor      string
	Summary    Summary
}

// PassEvent returns the event for the pass, and false when there is none to
// emit: a pass that changed nothing, was skipped, or was a dry run.
func (r Report) PassEvent() (PassEvent, bool) {
	if !r.Changed() {
		return PassEvent{}, false
	}
	return PassEvent{Kind: "retention.pass", Category: "audit", Visibility: "private", Actor: "janitor", Summary: r.Summary()}, true
}

// pass is the state of one Run.
type pass struct {
	j       *Janitor
	rep     *Report
	start   time.Time
	rows    int64
	stopped bool
}

func (p *pass) remainingTime() (time.Duration, bool) {
	if p.j.budget.MaxElapsed <= 0 {
		return 0, false
	}
	return p.j.budget.MaxElapsed - p.j.elapsed().Sub(p.start), true
}

func (p *pass) over() bool {
	b := p.j.budget
	if rem, ok := p.remainingTime(); (b.MaxRows > 0 && p.rows >= b.MaxRows) || (ok && rem <= 0) {
		p.stopped = true
	}
	return p.stopped
}

// op returns the bounds for the next store call: the per-transaction slice,
// capped by what is left of the pass budget. A dry-run count gets what is left
// of the time budget instead of the slice.
func (p *pass) op() Op {
	j := p.j
	op := Op{Limit: j.txRows, MaxDuration: j.txDur, DryRun: j.dryRun}
	if j.budget.MaxRows > 0 {
		if rem := j.budget.MaxRows - p.rows; rem < int64(op.Limit) {
			op.Limit = int(max(rem, 1))
		}
	}
	if rem, ok := p.remainingTime(); ok {
		rem = max(rem, time.Millisecond)
		if j.dryRun || rem < op.MaxDuration {
			op.MaxDuration = rem
		}
	}
	return op
}

// bounded returns a context limited to what is left of the time budget, for
// the reads that are not part of a write call.
func (p *pass) bounded(ctx context.Context) (context.Context, context.CancelFunc, bool) {
	rem, ok := p.remainingTime()
	if !ok {
		return ctx, func() {}, true
	}
	if rem <= 0 {
		return ctx, func() {}, false
	}
	c, cancel := context.WithTimeout(ctx, rem)
	return c, cancel, true
}

func isDeadline(err error) bool { return errors.Is(err, context.DeadlineExceeded) }

// Run does one pass over every stream. It returns a non-nil error only for
// failures that prevent the pass (listing streams, taking the lock, a
// cancelled context); a failing store call is counted in the class report and
// the pass goes on.
func (j *Janitor) Run(ctx context.Context, now time.Time) (Report, error) {
	return j.RunFrom(ctx, now, "")
}

// RunFrom is Run starting at the stream named by a Resume token ("" starts at the first).
func (j *Janitor) RunFrom(ctx context.Context, now time.Time, resume string) (rep Report, err error) {
	rep = Report{DryRun: j.dryRun, Trim: j.store.Caps().Trim, PolicyID: j.policyID}
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

// drain calls f until it reports no more work, the budget is spent, or ctx
// ends. The budget is checked before the first call too.
func (p *pass) drain(ctx context.Context, f func(Op) (rows int64, more bool, err error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.over() {
			return nil
		}
		rows, more, err := f(p.op())
		p.rows += rows
		if err != nil || !more || p.j.dryRun {
			return err
		}
	}
}

func (p *pass) stream(ctx context.Context, stream string, now time.Time) error {
	j, rep := p.j, p.rep
	st := j.store
	fail := func(class string, err error) {
		if err != nil && !errors.Is(err, context.Canceled) && !isDeadline(err) {
			rep.class(class).fail(err)
		}
	}
	step := func(class string, f func(Op) (int64, bool, error)) error {
		err := p.drain(ctx, f)
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		fail(class, err)
		return nil
	}
	plan := j.planner.Plan(now)

	// The steps, in order: classify -> roll up -> tombstone -> trim -> prune.
	// A step that finds the budget spent does nothing; the pass then goes
	// straight to collecting what it can.
	err := func() error {
		if classOf, ok := classifier(j.planner); ok {
			if err := step("", func(op Op) (int64, bool, error) {
				r, err := st.Classify(ctx, stream, classOf, op)
				rep.Classified += r.Rows
				return r.Rows, r.More, err
			}); err != nil {
				return err
			}
		}
		// Scanned is taken once rows have a class and before anything leaves.
		if c, cancel, ok := p.bounded(ctx); ok {
			if stats, err := st.Stats(c, stream); err != nil && !isDeadline(err) {
				fail("", err)
			} else if err == nil {
				for _, s := range stats {
					rep.class(s.Class).Scanned += s.Rows
				}
			}
			cancel()
		}
		for _, s := range plan.Rollups {
			if err := step(s.Class, func(op Op) (int64, bool, error) {
				r, err := st.Rollup(ctx, stream, s, op)
				c := rep.class(s.Class)
				c.RolledUp += r.Rows
				c.RollupBuckets += r.Buckets
				return r.Rows, r.More, err
			}); err != nil {
				return err
			}
		}
		for _, s := range plan.Tombstones {
			class := s.Class
			if s.Secret {
				class = SecretClass
			}
			if err := step(class, func(op Op) (int64, bool, error) {
				r, err := st.Tombstone(ctx, stream, s, op)
				c := rep.class(class)
				c.Tombstoned += r.Rows
				c.BytesFreed += r.BytesFreed
				return r.Rows, r.More, err
			}); err != nil {
				return err
			}
		}
		if len(plan.Trims) > 0 {
			var held int64
			if err := step("", func(op Op) (int64, bool, error) {
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
			}); err != nil {
				return err
			}
			rep.HeldByHead += held
		}
		for _, s := range plan.Prunes {
			if err := step(s.Class, func(op Op) (int64, bool, error) {
				r, err := st.Prune(ctx, stream, s, op)
				rep.class(s.Class).RollupsPruned += r.Rows
				return r.Rows, r.More, err
			}); err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		return err
	}
	return p.collect(ctx, stream, plan)
}

// collect gathers what is left: watermarks, the oldest remaining row and the
// backlog past tombstone age. Each read is bounded by what is left of the time
// budget; whatever cannot be read in time is marked Partial rather than left
// looking like zero.
func (p *pass) collect(ctx context.Context, stream string, plan Plan) error {
	j, rep := p.j, p.rep
	st := j.store
	partial := func() {
		for _, name := range classOrder {
			rep.class(name).Partial = true
		}
	}
	c, cancel, ok := p.bounded(ctx)
	defer cancel()
	if !ok {
		partial()
		return ctx.Err()
	}
	if j.store.Caps().Trim == TrimPerRow {
		if w, err := st.Watermarks(c, stream); err == nil {
			for class, v := range w {
				if cr := rep.class(class); v > cr.Watermark {
					cr.Watermark = v
				}
			}
		} else if isDeadline(err) {
			partial()
		}
	}
	if stats, err := st.Stats(c, stream); err == nil {
		for _, s := range stats {
			cr := rep.class(s.Class)
			if cr.OldestRemaining.IsZero() || (!s.Oldest.IsZero() && s.Oldest.Before(cr.OldestRemaining)) {
				cr.OldestRemaining = s.Oldest
			}
		}
	} else if isDeadline(err) {
		partial()
	}
	if !j.dryRun {
		for _, s := range plan.Tombstones {
			if s.Secret {
				continue
			}
			op := Op{DryRun: true}
			if rem, ok := p.remainingTime(); ok {
				op.MaxDuration = max(rem, time.Millisecond)
			}
			r, err := st.Tombstone(c, stream, s, op)
			switch {
			case err != nil && isDeadline(err), err == nil && r.More:
				rep.class(s.Class).Partial = true
			case err == nil:
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
