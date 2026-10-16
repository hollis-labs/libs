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
// what the janitor's own tests run on.
type Mem struct {
	mode retention.TrimMode

	mu      sync.Mutex
	locked  bool
	streams map[string]*memStream
}

type memRow struct {
	RowState
	dims    Dims
	size    int64
	trimmed bool
}

type bucketKey struct {
	class                                                         string
	size                                                          retention.Duration
	start                                                         int64
	kind, category, severity, project, workstream, agent, outcome string
}

type memStream struct {
	rows    []*memRow
	seq     uint64
	pruned  uint64
	buckets map[bucketKey]*Bucket
}

// NewMem returns an empty store with the given trim mode.
func NewMem(mode retention.TrimMode) *Mem {
	return &Mem{mode: mode, streams: map[string]*memStream{}}
}

var _ Fixture = (*Mem)(nil)

// Store returns m as a [retention.Store].
func (m *Mem) Store() retention.Store { return m }

func (m *Mem) stream(name string) *memStream {
	s := m.streams[name]
	if s == nil {
		s = &memStream{buckets: map[bucketKey]*Bucket{}}
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
		dims:     r.Dims, size: int64(len(r.Payload)) + 64,
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
func (m *Mem) Streams(context.Context) ([]string, error) {
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
func (m *Mem) TryLock(context.Context) (func(), bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locked {
		return nil, false, nil
	}
	m.locked = true
	return func() { m.mu.Lock(); m.locked = false; m.mu.Unlock() }, true, nil
}

// pick returns up to op.Limit rows for which match is true (all of them in a
// dry run) and whether more matched.
func pick(rows []*memRow, op retention.Op, match func(*memRow) bool) (picked []*memRow, more bool) {
	for _, r := range rows {
		if r.trimmed || !match(r) {
			continue
		}
		if !op.DryRun && op.Limit > 0 && len(picked) >= op.Limit {
			return picked, true
		}
		picked = append(picked, r)
	}
	return picked, false
}

// Classify implements [retention.Store].
func (m *Mem) Classify(_ context.Context, stream string, classOf func(retention.Attrs) string, op retention.Op) (retention.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stream(stream)
	rows, more := pick(s.rows, op, func(r *memRow) bool { return r.Class == "" })
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

// Rollup implements [retention.Store].
func (m *Mem) Rollup(_ context.Context, stream string, sp retention.RollupSpec, op retention.Op) (retention.RollupResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stream(stream)
	if len(sp.Buckets) == 0 {
		return retention.RollupResult{}, nil
	}
	largest := sp.Buckets[len(sp.Buckets)-1]
	closed := func(r *memRow) bool {
		return time.Unix(0, bucketStart(r.At, largest)+int64(largest)).Before(sp.ClosedBefore) ||
			time.Unix(0, bucketStart(r.At, largest)+int64(largest)).Equal(sp.ClosedBefore)
	}
	rows, more := pick(s.rows, op, func(r *memRow) bool {
		return !r.Rolled && r.Class == sp.Class && closed(r)
	})
	res := retention.RollupResult{Rows: int64(len(rows)), More: more}
	if op.DryRun {
		return res, nil
	}
	for _, r := range rows {
		for _, size := range sp.Buckets {
			k := bucketKey{class: r.Class, size: size, start: bucketStart(r.At, size), kind: r.dims.Kind, category: r.Attrs.Category,
				severity: r.Attrs.Severity, project: r.dims.Project, workstream: r.dims.Workstream, agent: r.dims.Agent, outcome: r.dims.Outcome}
			b := s.buckets[k]
			if b == nil {
				b = &Bucket{Class: r.Class, Size: size, Start: time.Unix(0, k.start).UTC(), Dims: r.dims, Category: r.Attrs.Category,
					Severity: r.Attrs.Severity, FirstSeq: r.Seq}
				s.buckets[k] = b
			}
			b.Count++
			b.FirstSeq, b.LastSeq = min(b.FirstSeq, r.Seq), max(b.LastSeq, r.Seq)
			res.Buckets++
		}
		r.Rolled = true
	}
	return res, nil
}

// Tombstone implements [retention.Store].
func (m *Mem) Tombstone(_ context.Context, stream string, sp retention.TombstoneSpec, op retention.Op) (retention.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stream(stream)
	rows, more := pick(s.rows, op, func(r *memRow) bool {
		if r.Tombstoned || r.At.After(sp.Before) {
			return false
		}
		if sp.Secret {
			return r.Attrs.Sensitivity == "secret"
		}
		return r.Class == sp.Class && r.Attrs.PII == sp.PII
	})
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

// Trim implements [retention.Store].
func (m *Mem) Trim(_ context.Context, stream string, specs []retention.TrimSpec, op retention.Op) (retention.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stream(stream)
	eligible := func(r *memRow) bool {
		for _, sp := range specs {
			if r.Class == sp.Class && !r.At.After(sp.Before) && (!sp.RequireRolled || r.Rolled) {
				return true
			}
		}
		return false
	}
	res := retention.Result{ByClass: map[string]int64{}}
	var removed []*memRow
	stoppedAt := -1
	for i, r := range s.rows {
		if r.trimmed {
			continue
		}
		if !eligible(r) {
			if m.mode == retention.TrimHeadOnly {
				stoppedAt = i
				break
			}
			continue
		}
		if !op.DryRun && op.Limit > 0 && len(removed) >= op.Limit {
			res.More = true
			break
		}
		removed = append(removed, r)
	}
	if m.mode == retention.TrimHeadOnly && stoppedAt >= 0 && !res.More {
		for _, r := range s.rows[stoppedAt:] {
			if !r.trimmed && eligible(r) {
				res.Held++
			}
		}
	}
	for _, r := range removed {
		res.Rows++
		res.BytesFreed += r.size
		res.ByClass[r.Class]++
		if op.DryRun {
			continue
		}
		r.trimmed = true
		if m.mode == retention.TrimHeadOnly {
			s.pruned = r.Seq
		}
	}
	return res, nil
}

// Prune implements [retention.Store].
func (m *Mem) Prune(_ context.Context, stream string, sp retention.PruneSpec, op retention.Op) (retention.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stream(stream)
	var res retention.Result
	for k := range s.buckets {
		if k.class != sp.Class || k.size != sp.Bucket || time.Unix(0, k.start+int64(k.size)).After(sp.Before) {
			continue
		}
		if !op.DryRun && op.Limit > 0 && res.Rows >= int64(op.Limit) {
			res.More = true
			break
		}
		res.Rows++
		if !op.DryRun {
			delete(s.buckets, k)
		}
	}
	return res, nil
}

// Stats implements [retention.Store].
func (m *Mem) Stats(_ context.Context, stream string) ([]retention.ClassStat, error) {
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
