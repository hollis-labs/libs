package streamhub_test

import (
	"context"
	"fmt"
	"iter"
	"sync"
	"testing"
	"time"

	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
	"github.com/hollis-labs/libs/ui-go/streamhub/hubtest"
)

// simpleLog is a second, deliberately different Log: it keeps its data in a
// store that outlives the log value (so Reopen is real), copies whole ranges
// instead of paging, and reports gaps as bare wrapped sentinels rather than
// *GapError. It stands in for the app-side, table-backed Log (Tether's
// store, Nanite's host feed) that the ported integration scenario exercises,
// and it checks that the suites and the hub do not depend on MemoryLog's
// details.
type simpleStore struct {
	mu      sync.Mutex
	streams map[string]*simpleStream
}

type simpleStream struct {
	recs    []streamhub.Record
	latest  streamhub.Seq
	pruned  streamhub.Seq
	dropped uint64
}

type simpleLog struct {
	store  *simpleStore
	mu     sync.Mutex
	closed bool
}

func newSimpleLog() *simpleLog {
	return &simpleLog{store: &simpleStore{streams: map[string]*simpleStream{}}}
}

func (l *simpleLog) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func (l *simpleLog) Append(ctx context.Context, stream string, e streamhub.Event) (streamhub.Record, error) {
	if err := ctx.Err(); err != nil {
		return streamhub.Record{}, err
	}
	if l.isClosed() {
		return streamhub.Record{}, streamhub.ErrClosed
	}
	s := l.store
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.streams[stream]
	if st == nil {
		st = &simpleStream{}
		s.streams[stream] = st
	}
	st.latest++
	r := streamhub.Record{Seq: st.latest, Name: e.Name, Data: append([]byte(nil), e.Data...), At: time.Now()}
	st.recs = append(st.recs, r)
	return r, nil
}

func (l *simpleLog) After(ctx context.Context, stream string, after streamhub.Seq) iter.Seq2[streamhub.Record, error] {
	return func(yield func(streamhub.Record, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(streamhub.Record{}, err)
			return
		}
		if l.isClosed() {
			yield(streamhub.Record{}, streamhub.ErrClosed)
			return
		}
		s := l.store
		s.mu.Lock()
		st := s.streams[stream]
		if st == nil {
			st = &simpleStream{}
		}
		var out []streamhub.Record
		var err error
		switch {
		case after > st.latest:
			err = fmt.Errorf("simplelog: after %d, head %d: %w", after, st.latest, streamhub.ErrCursorAhead)
		case after < st.pruned:
			err = fmt.Errorf("simplelog: after %d, pruned %d: %w", after, st.pruned, streamhub.ErrGap)
		default:
			out = append(out, st.recs[after-st.pruned:]...)
		}
		s.mu.Unlock()
		if err != nil {
			yield(streamhub.Record{}, err)
			return
		}
		for _, r := range out {
			if !yield(r, nil) {
				return
			}
		}
	}
}

func (l *simpleLog) Head(ctx context.Context, stream string) (streamhub.Head, error) {
	if err := ctx.Err(); err != nil {
		return streamhub.Head{}, err
	}
	if l.isClosed() {
		return streamhub.Head{}, streamhub.ErrClosed
	}
	s := l.store
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.streams[stream]
	if st == nil {
		return streamhub.Head{}, nil
	}
	return streamhub.Head{Latest: st.latest, PrunedThrough: st.pruned, Dropped: st.dropped}, nil
}

func (l *simpleLog) Trim(ctx context.Context, stream string, r streamhub.Retention) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.isClosed() {
		return streamhub.ErrClosed
	}
	s := l.store
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.streams[stream]
	if st == nil {
		return nil
	}
	cut := 0
	if r.MaxRecords > 0 && len(st.recs) > r.MaxRecords {
		cut = len(st.recs) - r.MaxRecords
	}
	if r.MaxBytes > 0 {
		total := 0
		for _, rec := range st.recs {
			total += len(rec.Name) + len(rec.Data)
		}
		i := 0
		for total > r.MaxBytes && i < len(st.recs) {
			total -= len(st.recs[i].Name) + len(st.recs[i].Data)
			i++
		}
		cut = max(cut, i)
	}
	st.recs = append([]streamhub.Record(nil), st.recs[cut:]...)
	st.pruned += streamhub.Seq(cut)
	st.dropped += uint64(cut)
	return nil
}

func (l *simpleLog) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return nil
}

func simpleFactory() hubtest.Factory {
	return hubtest.Factory{
		New: func(*testing.T) streamhub.Log { return newSimpleLog() },
		Reopen: func(_ *testing.T, l streamhub.Log) streamhub.Log {
			old := l.(*simpleLog)
			_ = old.Close()
			return &simpleLog{store: old.store}
		},
	}
}

func TestSimpleLogConformance(t *testing.T) { hubtest.Conformance(t, simpleFactory()) }

func TestSimpleLogHubSuite(t *testing.T) { hubtest.HubSuite(t, simpleFactory()) }
