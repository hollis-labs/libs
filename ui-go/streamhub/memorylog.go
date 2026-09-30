package streamhub

import (
	"context"
	"iter"
	"sync"
	"time"
)

const defaultPageSize uint64 = 128

// MemoryOption configures a [MemoryLog].
type MemoryOption func(*MemoryLog)

// WithPageSize sets how many records After copies per page (default 128).
// After never holds the log's lock while it yields, so a slow consumer of the
// iterator cannot stall Append.
func WithPageSize(n int) MemoryOption {
	return func(l *MemoryLog) {
		if n > 0 {
			l.pageSize = Seq(n)
		}
	}
}

// WithMemoryClock sets the clock used for Record.At and MaxAge trimming.
func WithMemoryClock(now func() time.Time) MemoryOption {
	return func(l *MemoryLog) {
		if now != nil {
			l.now = now
		}
	}
}

// WithMemoryRetention applies r to a stream after every Append, so the log
// stays bounded even when used without a Hub.
func WithMemoryRetention(r Retention) MemoryOption {
	return func(l *MemoryLog) { l.retention = r }
}

// MemoryLog is an in-memory [Log]: one bounded ring per stream. It is safe
// for concurrent use. Its records do not survive the process.
type MemoryLog struct {
	pageSize  Seq
	now       func() time.Time
	retention Retention

	mu      sync.Mutex
	streams map[string]*memStream
	closed  bool
}

type memStream struct {
	recs    []Record // recs[i].Seq == pruned+1+i
	latest  Seq
	pruned  Seq
	dropped uint64
	bytes   int
}

var _ Log = (*MemoryLog)(nil)
var _ Forgetter = (*MemoryLog)(nil)

// NewMemoryLog returns an empty in-memory log.
func NewMemoryLog(o ...MemoryOption) *MemoryLog {
	l := &MemoryLog{
		pageSize: Seq(defaultPageSize),
		now:      time.Now,
		streams:  make(map[string]*memStream),
	}
	for _, f := range o {
		f(l)
	}
	return l
}

func recSize(e Event) int { return len(e.Name) + len(e.Data) }

// Append implements [Log].
func (l *MemoryLog) Append(ctx context.Context, stream string, e Event) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	var data []byte
	if e.Data != nil {
		data = append([]byte(nil), e.Data...)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Record{}, ErrClosed
	}
	ms := l.streams[stream]
	if ms == nil {
		ms = &memStream{}
		l.streams[stream] = ms
	}
	ms.latest++
	rec := Record{Seq: ms.latest, Name: e.Name, Data: data, At: l.now()}
	ms.recs = append(ms.recs, rec)
	ms.bytes += recSize(e)
	if !l.retention.isZero() {
		l.trimLocked(ms, l.retention)
	}
	return rec, nil
}

// After implements [Log].
func (l *MemoryLog) After(ctx context.Context, stream string, after Seq) iter.Seq2[Record, error] {
	return func(yield func(Record, error) bool) {
		cursor := after
		for {
			page, err := l.page(ctx, stream, cursor)
			if err != nil {
				yield(Record{}, err)
				return
			}
			if len(page) == 0 {
				return
			}
			for _, r := range page {
				if !yield(r, nil) {
					return
				}
			}
			cursor = page[len(page)-1].Seq
		}
	}
}

// page returns up to pageSize records after cursor, or a *GapError.
func (l *MemoryLog) page(ctx context.Context, stream string, cursor Seq) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, ErrClosed
	}
	ms := l.streams[stream]
	if ms == nil {
		ms = &memStream{}
	}
	switch {
	case cursor > ms.latest:
		return nil, &GapError{Gap: Gap{
			Reason: GapCursorAhead, Requested: cursor,
			OldestAvailable: ms.pruned + 1, Latest: ms.latest,
			Missed: uint64(cursor - ms.latest),
		}}
	case cursor < ms.pruned:
		return nil, &GapError{Gap: Gap{
			Reason: GapRetention, Requested: cursor,
			OldestAvailable: ms.pruned + 1, Latest: ms.latest,
			Missed: uint64(ms.pruned - cursor),
		}}
	}
	// Slice indices may be any integer type; cursor is in [pruned, latest],
	// so the offsets are within len(recs).
	start := cursor - ms.pruned
	end := min(start+l.pageSize, Seq(len(ms.recs)))
	return append([]Record(nil), ms.recs[start:end]...), nil
}

// Head implements [Log].
func (l *MemoryLog) Head(ctx context.Context, stream string) (Head, error) {
	if err := ctx.Err(); err != nil {
		return Head{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Head{}, ErrClosed
	}
	ms := l.streams[stream]
	if ms == nil {
		return Head{}, nil
	}
	return Head{Latest: ms.latest, PrunedThrough: ms.pruned, Dropped: ms.dropped}, nil
}

// Trim implements [Log].
func (l *MemoryLog) Trim(ctx context.Context, stream string, r Retention) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	if ms := l.streams[stream]; ms != nil {
		l.trimLocked(ms, r)
	}
	return nil
}

func (l *MemoryLog) trimLocked(ms *memStream, r Retention) {
	n := len(ms.recs)
	cut := 0
	if r.MaxRecords > 0 && n > r.MaxRecords {
		cut = n - r.MaxRecords
	}
	if r.MaxBytes > 0 {
		total, i := ms.bytes, 0
		for total > r.MaxBytes && i < n {
			total -= len(ms.recs[i].Name) + len(ms.recs[i].Data)
			i++
		}
		cut = max(cut, i)
	}
	if r.MaxAge > 0 {
		now := l.now()
		i := 0
		for i < n && now.Sub(ms.recs[i].At) > r.MaxAge {
			i++
		}
		cut = max(cut, i)
	}
	if cut == 0 {
		return
	}
	for i := range cut {
		ms.bytes -= len(ms.recs[i].Name) + len(ms.recs[i].Data)
	}
	clear(ms.recs[:cut])
	ms.recs = ms.recs[cut:]
	ms.pruned += Seq(cut)
	ms.dropped += uint64(cut)
}

// Forget implements [Forgetter]: it discards the stream, Seq counter included.
func (l *MemoryLog) Forget(_ context.Context, stream string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.streams, stream)
	return nil
}

// Close implements [Log]. Records are discarded.
func (l *MemoryLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.streams = make(map[string]*memStream)
	return nil
}
