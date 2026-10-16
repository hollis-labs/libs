package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention"
)

// Mem is an in-memory [Fixture]: a reference implementation of
// [retention.Store] that defines the contract in executable form. It is also
// what the janitor's own tests run on. It honours cancellation and
// Op.MaxDuration, supports row holds and fault injection, and its writes are
// atomic: a failed call changes nothing.
type Mem struct {
	mode retention.TrimMode

	mu      sync.Mutex
	locked  bool
	faults  map[string]error
	streams map[string]*memStream
}

type memRow struct {
	RowState
	dims    Dims
	size    int64
	hold    bool
	trimmed bool
}

type bucketKey struct {
	class                                                         string
	size                                                          retention.Duration
	start                                                         int64
	kind, category, severity, project, workstream, agent, outcome string
}

type memStream struct {
	rows       []*memRow
	seq        uint64
	pruned     uint64
	buckets    map[bucketKey]*Bucket
	watermarks map[string]uint64
}

// NewMem returns an empty store with the given trim mode.
func NewMem(mode retention.TrimMode) *Mem {
	return &Mem{mode: mode, streams: map[string]*memStream{}, faults: map[string]error{}}
}

var (
	_ Fixture     = (*Mem)(nil)
	_ Faulty      = (*Mem)(nil)
	_ HoldSupport = (*Mem)(nil)
	_ TimeBounded = (*Mem)(nil)
)

// SupportsHold implements [HoldSupport].
func (m *Mem) SupportsHold() bool { return true }

// HonoursMaxDuration implements [TimeBounded].
func (m *Mem) HonoursMaxDuration() bool { return true }

// InjectFault implements [Faulty].
func (m *Mem) InjectFault(point string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		delete(m.faults, point)
		return
	}
	m.faults[point] = err
}

// Store returns m as a [retention.Store].
func (m *Mem) Store() retention.Store { return m }

func (m *Mem) stream(name string) *memStream {
	s := m.streams[name]
	if s == nil {
		s = &memStream{buckets: map[bucketKey]*Bucket{}, watermarks: map[string]uint64{}}
		m.streams[name] = s
	}
	return s
}

// Add implements [Fixture].
func (m *Mem) Add(r Row) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stream(r.Stream)
	s.seq++
	s.rows = append(s.rows, &memRow{
		RowState: RowState{Seq: s.seq, Class: r.Class, PolicyID: r.PolicyID, At: r.At.UTC(), Attrs: r.Attrs, Payload: append([]byte(nil), r.Payload...)},
		dims:     r.Dims, size: int64(len(r.Payload)) + 64, hold: r.Hold,
	})
	return s.seq, nil
}

// Rows implements [Fixture].
func (m *Mem) Rows(stream string) []RowState {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []RowState
	if s := m.streams[stream]; s != nil {
		for _, r := range s.rows {
			if !r.trimmed {
				out = append(out, r.RowState)
			}
		}
	}
	return out
}

// Buckets implements [Fixture].
func (m *Mem) Buckets(stream string) []Bucket {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Bucket
	if s := m.streams[stream]; s != nil {
		for _, b := range s.buckets {
			out = append(out, *b)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		if out[i].Size != out[j].Size {
			return out[i].Size < out[j].Size
		}
		return out[i].Start.Before(out[j].Start)
	})
	return out
}

// PrunedThrough implements [Fixture].
func (m *Mem) PrunedThrough(stream string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.streams[stream]; s != nil {
		return s.pruned
	}
	return 0
}

// Caps implements [retention.Store].
func (m *Mem) Caps() retention.Caps { return retention.Caps{Trim: m.mode} }

// Streams implements [retention.Store].
func (m *Mem) Streams(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for name, s := range m.streams {
		if len(s.rows) > 0 || len(s.buckets) > 0 {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// TryLock implements [retention.Store].
func (m *Mem) TryLock(ctx context.Context) (func(), bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locked {
		return nil, false, nil
	}
	m.locked = true
	return func() { m.mu.Lock(); m.locked = false; m.mu.Unlock() }, true, nil
}

// budget enforces Op.Limit and Op.MaxDuration for a row-by-row call: it makes
// progress on at least one row before it reports the call as over.
type budget struct {
	op    retention.Op
	start time.Time
	n     int
}

func newBudget(op retention.Op) *budget { return &budget{op: op, start: time.Now()} }

func (b *budget) full() bool {
	if b.n == 0 {
		return false
	}
	// A dry run ignores the row limit but not the time bound.
	if !b.op.DryRun && b.op.Limit > 0 && b.n >= b.op.Limit {
		return true
	}
	return b.op.MaxDuration > 0 && time.Since(b.start) >= b.op.MaxDuration
}

// pick returns the rows for which match is true within the budget, and whether more matched.
func pick(ctx context.Context, rows []*memRow, op retention.Op, match func(*memRow) bool) (picked []*memRow, more bool, err error) {
	b := newBudget(op)
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if r.trimmed || r.hold || !match(r) {
			continue
		}
		if b.full() {
			return picked, true, nil
		}
		picked = append(picked, r)
		b.n++
	}
	return picked, false, nil
}

// Classify implements [retention.Store].
func (m *Mem) Classify(ctx context.Context, stream string, classOf func(retention.Attrs) string, op retention.Op) (retention.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stream(stream)
	rows, more, err := pick(ctx, s.rows, op, func(r *memRow) bool { return r.Class == "" })
	if err != nil {
		return retention.Result{}, err
	}
	if !op.DryRun {
		for _, r := range rows {
			r.Class = classOf(r.Attrs)
		}
	}
	return retention.Result{Rows: int64(len(rows)), More: more}, nil
}

func bucketStart(at time.Time, size retention.Duration) int64 {
	n := int64(size)
	u := at.UnixNano()
	q := u / n
	if u < 0 && u%n != 0 {
		q--
	}
	return q * n
}

// Rollup implements [retention.Store]. The counts and the rolled flags are
// applied together or not at all.
func (m *Mem) Rollup(ctx context.Context, stream string, sp retention.RollupSpec, op retention.Op) (retention.RollupResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stream(stream)
	if len(sp.Buckets) == 0 {
		return retention.RollupResult{}, nil
	}
	largest := sp.Buckets[len(sp.Buckets)-1]
	closed := func(r *memRow) bool {
		end := time.Unix(0, bucketStart(r.At, largest)+int64(largest))
		return !end.After(sp.ClosedBefore)
	}
	rows, more, err := pick(ctx, s.rows, op, func(r *memRow) bool { return !r.Rolled && r.Class == sp.Class && closed(r) })
	if err != nil {
		return retention.RollupResult{}, err
	}
	res := retention.RollupResult{Rows: int64(len(rows)), More: more}
	if op.DryRun {
		return res, nil
	}
	scratch := map[bucketKey]*Bucket{}
	for k, b := range s.buckets {
		c := *b
		scratch[k] = &c
	}
	for _, r := range rows {
		for _, size := range sp.Buckets {
			k := bucketKey{class: r.Class, size: size, start: bucketStart(r.At, size), kind: r.dims.Kind, category: r.Attrs.Category,
				severity: r.Attrs.Severity, project: r.dims.Project, workstream: r.dims.Workstream, agent: r.dims.Agent, outcome: r.dims.Outcome}
			b := scratch[k]
			if b == nil {
				b = &Bucket{Class: r.Class, Size: size, Start: time.Unix(0, k.start).UTC(), Dims: r.dims, Category: r.Attrs.Category,
					Severity: r.Attrs.Severity, FirstSeq: r.Seq}
				scratch[k] = b
			}
			b.Count++
			b.FirstSeq, b.LastSeq = min(b.FirstSeq, r.Seq), max(b.LastSeq, r.Seq)
			res.Buckets++
		}
	}
	// A fault between counting and flagging rolls the whole call back.
	if err := m.faults[FaultRollup]; err != nil {
		return retention.RollupResult{}, err
	}
	s.buckets = scratch
	for _, r := range rows {
		r.Rolled = true
	}
	return res, nil
}

// Tombstone implements [retention.Store].
func (m *Mem) Tombstone(ctx context.Context, stream string, sp retention.TombstoneSpec, op retention.Op) (retention.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stream(stream)
	rows, more, err := pick(ctx, s.rows, op, func(r *memRow) bool {
		if r.Tombstoned || r.At.After(sp.Before) {
			return false
		}
		if sp.Secret {
			return r.Attrs.Sensitivity == "secret"
		}
		return r.Class == sp.Class && r.Attrs.PII == sp.PII
	})
	if err != nil {
		return retention.Result{}, err
	}
	res := retention.Result{Rows: int64(len(rows)), More: more}
	for _, r := range rows {
		res.BytesFreed += int64(len(r.Payload))
		if op.DryRun {
			continue
		}
		if sp.Withhold {
			r.PayloadDigest = "withheld"
		} else {
			sum := sha256.Sum256(r.Payload)
			r.PayloadDigest = "sha256:" + hex.EncodeToString(sum[:])
		}
		r.Payload, r.Tombstoned = nil, true
	}
	return res, nil
}

// Trim implements [retention.Store]. Removal and the per-class watermark (or
// pruned-through) are applied together or not at all. R-audit rows are never
// removed, whatever the specs say.
func (m *Mem) Trim(ctx context.Context, stream string, specs []retention.TrimSpec, op retention.Op) (retention.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stream(stream)
	eligible := func(r *memRow) bool {
		if r.hold || r.Class == retention.ClassAudit {
			return false
		}
		for _, sp := range specs {
			// A per-row store fails closed: no removal without a committed covering
			// rollup, whatever RequireRolled says (D7 rev 2.3).
			needRolled := sp.RequireRolled || m.mode == retention.TrimPerRow
			if r.Class == sp.Class && !r.At.After(sp.Before) && (!needRolled || r.Rolled) {
				return true
			}
		}
		return false
	}
	res := retention.Result{ByClass: map[string]int64{}}
	var removed []*memRow
	stoppedAt := -1
	blocked := map[string]bool{} // per-row: a class stops at its first row that must stay
	b := newBudget(op)
	for i, r := range s.rows {
		if err := ctx.Err(); err != nil {
			return retention.Result{}, err
		}
		if r.trimmed {
			continue
		}
		if m.mode == retention.TrimPerRow && r.Class == retention.ClassAudit {
			continue // audit never blocks other classes and is never removed
		}
		if !eligible(r) {
			if m.mode == retention.TrimHeadOnly {
				stoppedAt = i
				break
			}
			blocked[r.Class] = true
			continue
		}
		if m.mode == retention.TrimPerRow && blocked[r.Class] {
			continue // cursor order per class: a later row never goes before an earlier one
		}
		if b.full() {
			res.More = true
			break
		}
		removed = append(removed, r)
		b.n++
	}
	if m.mode == retention.TrimHeadOnly && stoppedAt >= 0 && !res.More {
		for _, r := range s.rows[stoppedAt:] {
			if !r.trimmed && eligible(r) {
				res.Held++
			}
		}
	}
	// Work on copies of the persisted state so a fault rolls everything back.
	pruned, marks := s.pruned, map[string]uint64{}
	for c, w := range s.watermarks {
		marks[c] = w
	}
	for _, r := range removed {
		res.Rows++
		res.BytesFreed += r.size
		res.ByClass[r.Class]++
		if m.mode == retention.TrimHeadOnly {
			pruned = r.Seq
		} else if r.Seq > marks[r.Class] {
			marks[r.Class] = r.Seq
		}
	}
	if op.DryRun {
		return res, nil
	}
	if err := m.faults[FaultTrim]; err != nil && len(removed) > 0 {
		return retention.Result{}, err
	}
	for _, r := range removed {
		r.trimmed = true
	}
	s.pruned, s.watermarks = pruned, marks
	return res, nil
}

// Watermarks implements [retention.Store].
func (m *Mem) Watermarks(ctx context.Context, stream string) (map[string]uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]uint64{}
	if s := m.streams[stream]; s != nil {
		for c, w := range s.watermarks {
			out[c] = w
		}
	}
	return out, nil
}

// Prune implements [retention.Store].
func (m *Mem) Prune(ctx context.Context, stream string, sp retention.PruneSpec, op retention.Op) (retention.Result, error) {
	if err := ctx.Err(); err != nil {
		return retention.Result{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stream(stream)
	var res retention.Result
	b := newBudget(op)
	for k := range s.buckets {
		if err := ctx.Err(); err != nil {
			return retention.Result{}, err
		}
		if k.class != sp.Class || k.size != sp.Bucket || time.Unix(0, k.start+int64(k.size)).After(sp.Before) {
			continue
		}
		if b.full() {
			res.More = true
			break
		}
		res.Rows++
		b.n++
		if !op.DryRun {
			delete(s.buckets, k)
		}
	}
	return res, nil
}

// Stats implements [retention.Store].
func (m *Mem) Stats(ctx context.Context, stream string) ([]retention.ClassStat, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	by := map[string]*retention.ClassStat{}
	if s := m.streams[stream]; s != nil {
		for _, r := range s.rows {
			if r.trimmed {
				continue
			}
			c := by[r.Class]
			if c == nil {
				c = &retention.ClassStat{Class: r.Class}
				by[r.Class] = c
			}
			c.Rows++
			if c.Oldest.IsZero() || r.At.Before(c.Oldest) {
				c.Oldest = r.At
			}
		}
	}
	var out []retention.ClassStat
	for _, c := range by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Class < out[j].Class })
	return out, nil
}
