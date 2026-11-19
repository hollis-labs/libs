package sqlitestore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/hollis-labs/libs/core/store"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/logkit"
)

func (s *Store) record(seq uint64, name string, atNS int64, data []byte, id string) streamhub.Record {
	if len(data) == 0 {
		data = nil
	}
	return streamhub.Record{Seq: streamhub.Seq(seq), Name: name, Data: data, At: time.Unix(0, atNS).UTC(), ID: id, Epoch: s.epoch}
}

// Append implements streamhub.Log.
func (s *Store) Append(ctx context.Context, stream string, e streamhub.Event) (streamhub.Record, error) {
	if err := s.enter(ctx); err != nil {
		return streamhub.Record{}, err
	}
	defer s.leave()
	data := bytes.Clone(e.Data)
	digest := logkit.Digest(e)
	var out streamhub.Record
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if e.ID != "" {
			var (
				ostream, oname string
				oseq           uint64
				oat            int64
				odigest, odata []byte
			)
			err := tx.QueryRowContext(ctx,
				`SELECT stream, seq, name, at_ns, digest, data FROM chat_records WHERE event_id = ?`, e.ID).
				Scan(&ostream, &oseq, &oname, &oat, &odigest, &odata)
			switch {
			case err == nil:
				if ostream != stream || !bytes.Equal(odigest, digest) {
					return streamhub.ErrEventIDConflict
				}
				out = s.record(oseq, oname, oat, odata, e.ID)
				return nil
			case !noRows(err):
				return err
			}
		}
		var seq uint64
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO chat_streams(stream, latest) VALUES(?, 1)
			 ON CONFLICT(stream) DO UPDATE SET latest = latest + 1 RETURNING latest`, stream).Scan(&seq); err != nil {
			return err
		}
		at := s.now().UTC().UnixNano()
		var eid any
		if e.ID != "" {
			eid = e.ID
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chat_records(stream, seq, event_id, name, at_ns, size, data, digest) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			stream, seq, eid, e.Name, at, len(e.Name)+len(data), data, digest); err != nil {
			return err
		}
		out = s.record(seq, e.Name, at, data, e.ID)
		return nil
	})
	if err != nil {
		return streamhub.Record{}, fmt.Errorf("sqlitestore: append to %q: %w", stream, err)
	}
	return out, nil
}

// After implements streamhub.Log.
func (s *Store) After(ctx context.Context, stream string, after streamhub.Seq) iter.Seq2[streamhub.Record, error] {
	return s.AfterEpoch(ctx, stream, "", after)
}

// AfterEpoch implements streamhub.EpochReader.
func (s *Store) AfterEpoch(ctx context.Context, stream, epoch string, after streamhub.Seq) iter.Seq2[streamhub.Record, error] {
	return func(yield func(streamhub.Record, error) bool) {
		cursor := after
		for {
			recs, err := s.readPage(ctx, stream, epoch, cursor)
			if err != nil {
				yield(streamhub.Record{}, err)
				return
			}
			if len(recs) == 0 {
				return
			}
			for _, r := range recs {
				if !yield(r, nil) {
					return
				}
			}
			cursor = recs[len(recs)-1].Seq
		}
	}
}

// readPage reads one page in a snapshot, so a concurrent Trim can never make
// it return records together with a gap, or records past a hole.
func (s *Store) readPage(ctx context.Context, stream, epoch string, cursor streamhub.Seq) ([]streamhub.Record, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	defer s.leave()
	var out []streamhub.Record
	err := s.db.ReadTx(ctx, func(ctx context.Context, q store.Querier) error {
		out = out[:0]
		var latest, pruned uint64
		if err := q.QueryRowContext(ctx, `SELECT latest, pruned FROM chat_streams WHERE stream = ?`, stream).
			Scan(&latest, &pruned); err != nil && !noRows(err) {
			return err
		}
		gap := func(reason streamhub.GapReason, missed uint64) error {
			return &streamhub.GapError{Gap: streamhub.Gap{
				Reason: reason, Requested: cursor, OldestAvailable: streamhub.Seq(pruned + 1),
				Latest: streamhub.Seq(latest), Missed: missed, Epoch: s.epoch,
			}}
		}
		switch {
		case epoch != "" && epoch != s.epoch:
			return gap(streamhub.GapEpochChanged, 0)
		case uint64(cursor) > latest:
			return gap(streamhub.GapCursorAhead, uint64(cursor)-latest)
		case uint64(cursor) < pruned:
			return gap(streamhub.GapRetention, pruned-uint64(cursor))
		}
		rows, err := q.QueryContext(ctx,
			`SELECT seq, name, at_ns, data, event_id FROM chat_records WHERE stream = ? AND seq > ? ORDER BY seq LIMIT ?`,
			stream, uint64(cursor), s.page)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				seq  uint64
				name string
				at   int64
				data []byte
				eid  sql.NullString
			)
			if err := rows.Scan(&seq, &name, &at, &data, &eid); err != nil {
				return err
			}
			out = append(out, s.record(seq, name, at, data, eid.String))
		}
		return rows.Err()
	})
	if err != nil {
		var ge *streamhub.GapError
		if errors.As(err, &ge) {
			return nil, ge // the hub matches the gap itself, unwrapped
		}
		return nil, fmt.Errorf("sqlitestore: read %q: %w", stream, err)
	}
	return out, nil
}

// Head implements streamhub.Log.
func (s *Store) Head(ctx context.Context, stream string) (streamhub.Head, error) {
	if err := s.enter(ctx); err != nil {
		return streamhub.Head{}, err
	}
	defer s.leave()
	h := streamhub.Head{Epoch: s.epoch}
	var latest, pruned, dropped uint64
	err := s.db.QueryRowContext(ctx, `SELECT latest, pruned, dropped FROM chat_streams WHERE stream = ?`, stream).
		Scan(&latest, &pruned, &dropped)
	if noRows(err) {
		return h, nil
	}
	if err != nil {
		return streamhub.Head{}, fmt.Errorf("sqlitestore: head of %q: %w", stream, err)
	}
	h.Latest, h.PrunedThrough, h.Dropped = streamhub.Seq(latest), streamhub.Seq(pruned), dropped
	return h, nil
}

// Trim implements streamhub.Log. Records leave from the oldest end only, so
// the cursor space stays contiguous after PrunedThrough.
func (s *Store) Trim(ctx context.Context, stream string, r streamhub.Retention) error {
	if err := s.enter(ctx); err != nil {
		return err
	}
	defer s.leave()
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var latest, pruned uint64
		if err := tx.QueryRowContext(ctx, `SELECT latest, pruned FROM chat_streams WHERE stream = ?`, stream).
			Scan(&latest, &pruned); err != nil {
			if noRows(err) {
				return nil
			}
			return err
		}
		cut := pruned
		if r.MaxRecords > 0 && latest-pruned > uint64(r.MaxRecords) {
			cut = max(cut, latest-uint64(r.MaxRecords))
		}
		if r.MaxAge > 0 {
			var c sql.NullInt64
			before := s.now().Add(-r.MaxAge).UnixNano()
			if err := tx.QueryRowContext(ctx,
				`SELECT MAX(seq) FROM chat_records WHERE stream = ? AND seq > ? AND at_ns < ?`, stream, pruned, before).Scan(&c); err != nil {
				return err
			}
			if c.Valid && c.Int64 > 0 {
				cut = max(cut, uint64(c.Int64))
			}
		}
		if r.MaxBytes > 0 {
			over, err := oldestOverBytes(ctx, tx, stream, pruned, r.MaxBytes)
			if err != nil {
				return err
			}
			cut = max(cut, over)
		}
		if cut <= pruned {
			return nil
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM chat_records WHERE stream = ? AND seq <= ?`, stream, cut)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE chat_streams SET pruned = ?, dropped = dropped + ? WHERE stream = ?`, cut, n, stream)
		return err
	})
	if err != nil {
		return fmt.Errorf("sqlitestore: trim %q: %w", stream, err)
	}
	return nil
}

// oldestOverBytes walks the stream newest first and returns the seq of the
// first record that takes the total past limit: everything up to it goes.
func oldestOverBytes(ctx context.Context, tx store.Tx, stream string, pruned uint64, limit int) (uint64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT seq, size FROM chat_records WHERE stream = ? AND seq > ? ORDER BY seq DESC`, stream, pruned)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	total := 0
	for rows.Next() {
		var (
			seq  uint64
			size int
		)
		if err := rows.Scan(&seq, &size); err != nil {
			return 0, err
		}
		if total += size; total > limit {
			return seq, nil
		}
	}
	return 0, rows.Err()
}
