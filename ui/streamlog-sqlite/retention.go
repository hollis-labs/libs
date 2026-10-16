package streamlogsqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention"
)

// RetentionStore returns the log as a [retention.Store], so a
// [retention.Janitor] can tombstone, roll up, trim and prune it. Trim is
// head-only: it advances the stream's pruned-through cursor exactly like
// [Log.Trim], so readers behind it get an ordinary retention gap.
func (l *Log) RetentionStore() retention.Store { return &retentionStore{l: l} }

type retentionStore struct {
	l    *Log
	lock sync.Mutex
	held bool
}

var _ retention.Store = (*retentionStore)(nil)

func (s *retentionStore) Caps() retention.Caps { return retention.Caps{Trim: retention.TrimHeadOnly} }

// TryLock implements [retention.Store]. One process owns the file, so an
// in-process flag is the whole lock.
func (s *retentionStore) TryLock(context.Context) (func(), bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.held {
		return nil, false, nil
	}
	s.held = true
	return func() { s.lock.Lock(); s.held = false; s.lock.Unlock() }, true, nil
}

func (s *retentionStore) Streams(ctx context.Context) ([]string, error) {
	if err := s.l.enter(); err != nil {
		return nil, err
	}
	defer s.l.leave()
	rows, err := s.l.r.QueryContext(ctx, `SELECT stream FROM streams UNION SELECT stream FROM rollups ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (s *retentionStore) Stats(ctx context.Context, stream string) ([]retention.ClassStat, error) {
	if err := s.l.enter(); err != nil {
		return nil, err
	}
	defer s.l.leave()
	rows, err := s.l.r.QueryContext(ctx, `SELECT class, count(*), min(at_ns) FROM records WHERE stream=? GROUP BY class ORDER BY class`, stream)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []retention.ClassStat
	for rows.Next() {
		var c retention.ClassStat
		var oldest int64
		if err := rows.Scan(&c.Class, &c.Rows, &oldest); err != nil {
			return nil, err
		}
		c.Oldest = time.Unix(0, oldest).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// tx runs f in one write transaction on the single writer connection.
func (s *retentionStore) tx(ctx context.Context, f func(*sql.Tx) error) error {
	if err := s.l.enter(); err != nil {
		return err
	}
	defer s.l.leave()
	tx, err := s.l.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// dry runs a counting query for a dry-run call under the call's MaxDuration. A
// count that cannot finish in time is not an error: it reports More with
// whatever lower bound was reached (none).
func (s *retentionStore) dry(ctx context.Context, op retention.Op, f func(context.Context) error) (more bool, err error) {
	if op.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, op.MaxDuration)
		defer cancel()
	}
	if err := f(ctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) && ctx.Err() == context.DeadlineExceeded {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func limitOf(op retention.Op) int {
	if op.Limit > 0 {
		return op.Limit
	}
	return 1000
}

func (s *retentionStore) Classify(ctx context.Context, stream string, classOf func(retention.Attrs) string, op retention.Op) (retention.Result, error) {
	var res retention.Result
	if op.DryRun {
		var err error
		res.More, err = s.dry(ctx, op, func(c context.Context) error {
			return s.l.r.QueryRowContext(c, `SELECT count(*) FROM records WHERE stream=? AND class=''`, stream).Scan(&res.Rows)
		})
		return res, err
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT category, severity, sensitivity, pii FROM records WHERE stream=? AND class=''`, stream)
		if err != nil {
			return err
		}
		type combo struct {
			cat, sev, sens string
			pii            bool
		}
		var combos []combo
		for rows.Next() {
			var c combo
			if err := rows.Scan(&c.cat, &c.sev, &c.sens, &c.pii); err != nil {
				_ = rows.Close()
				return err
			}
			combos = append(combos, c)
		}
		_ = rows.Close()
		budget := limitOf(op)
		for _, c := range combos {
			if budget <= 0 {
				break
			}
			class := classOf(retention.Attrs{Category: c.cat, Severity: c.sev, Sensitivity: c.sens, PII: c.pii})
			r, err := tx.ExecContext(ctx, `UPDATE records SET class=? WHERE (stream, seq) IN
				(SELECT stream, seq FROM records WHERE stream=? AND class='' AND category=? AND severity=? AND sensitivity=? AND pii=? LIMIT ?)`,
				class, stream, c.cat, c.sev, c.sens, c.pii, budget)
			if err != nil {
				return err
			}
			n, _ := r.RowsAffected()
			res.Rows += n
			budget -= int(n)
		}
		var more int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM records WHERE stream=? AND class='')`, stream).Scan(&more); err != nil {
			return err
		}
		res.More = more == 1
		return nil
	})
	return res, err
}

// closedBound is the exclusive upper bound on a record's time for the
// largest bucket it falls in to have closed by closedBefore.
func closedBound(largest retention.Duration, closedBefore time.Time) int64 {
	n := int64(largest)
	c := closedBefore.UnixNano()
	q := c / n
	if c < 0 && c%n != 0 {
		q--
	}
	return q * n
}

func bucketFloor(at int64, size retention.Duration) int64 {
	n := int64(size)
	q := at / n
	if at < 0 && at%n != 0 {
		q--
	}
	return q * n
}

type rollKey struct {
	bucketNS, start                                               int64
	kind, category, severity, project, workstream, agent, outcome string
}

type rollVal struct{ count, first, last int64 }

func (s *retentionStore) Rollup(ctx context.Context, stream string, sp retention.RollupSpec, op retention.Op) (retention.RollupResult, error) {
	var res retention.RollupResult
	if len(sp.Buckets) == 0 {
		return res, nil
	}
	bound := closedBound(sp.Buckets[len(sp.Buckets)-1], sp.ClosedBefore)
	const where = `stream=? AND rolled=0 AND class=? AND at_ns < ?`
	if op.DryRun {
		var err error
		res.More, err = s.dry(ctx, op, func(c context.Context) error {
			return s.l.r.QueryRowContext(c, `SELECT count(*) FROM records WHERE `+where, stream, sp.Class, bound).Scan(&res.Rows)
		})
		return res, err
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT seq, at_ns, kind, category, severity, project_id, workstream_id, agent_urn, outcome
			FROM records WHERE `+where+` ORDER BY seq LIMIT ?`, stream, sp.Class, bound, limitOf(op)+1)
		if err != nil {
			return err
		}
		type rec struct {
			seq, at                                                       int64
			kind, category, severity, project, workstream, agent, outcome string
		}
		var picked []rec
		for rows.Next() {
			var r rec
			if err := rows.Scan(&r.seq, &r.at, &r.kind, &r.category, &r.severity, &r.project, &r.workstream, &r.agent, &r.outcome); err != nil {
				_ = rows.Close()
				return err
			}
			picked = append(picked, r)
		}
		_ = rows.Close()
		if len(picked) > limitOf(op) {
			picked, res.More = picked[:limitOf(op)], true
		}
		agg := map[rollKey]*rollVal{}
		for _, r := range picked {
			for _, size := range sp.Buckets {
				k := rollKey{bucketNS: int64(size), start: bucketFloor(r.at, size), kind: r.kind, category: r.category, severity: r.severity,
					project: r.project, workstream: r.workstream, agent: r.agent, outcome: r.outcome}
				v := agg[k]
				if v == nil {
					agg[k] = &rollVal{count: 1, first: r.seq, last: r.seq}
					continue
				}
				v.count++
				v.first, v.last = min(v.first, r.seq), max(v.last, r.seq)
			}
		}
		for k, v := range agg {
			if _, err := tx.ExecContext(ctx, `INSERT INTO rollups(stream, class, bucket_ns, bucket_start, kind, category, severity, project_id, workstream_id, agent_urn, outcome, count, first_seq, last_seq)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(stream, class, bucket_ns, bucket_start, kind, category, severity, project_id, workstream_id, agent_urn, outcome)
				DO UPDATE SET count = count + excluded.count, first_seq = min(first_seq, excluded.first_seq), last_seq = max(last_seq, excluded.last_seq)`,
				stream, sp.Class, k.bucketNS, k.start, k.kind, k.category, k.severity, k.project, k.workstream, k.agent, k.outcome, v.count, v.first, v.last); err != nil {
				return err
			}
			res.Buckets++
		}
		if len(picked) > 0 {
			ids := make([]string, len(picked))
			args := make([]any, 0, len(picked)+1)
			args = append(args, stream)
			for i, r := range picked {
				ids[i] = "?"
				args = append(args, r.seq)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE records SET rolled=1 WHERE stream=? AND seq IN (`+strings.Join(ids, ",")+`)`, args...); err != nil {
				return err
			}
		}
		res.Rows = int64(len(picked))
		return nil
	})
	return res, err
}

func (s *retentionStore) Tombstone(ctx context.Context, stream string, sp retention.TombstoneSpec, op retention.Op) (retention.Result, error) {
	var res retention.Result
	where := `stream=? AND tier=0 AND at_ns <= ?`
	args := []any{stream, sp.Before.UnixNano()}
	if sp.Secret {
		where += ` AND sensitivity='secret'`
	} else {
		where += ` AND class=? AND pii=?`
		args = append(args, sp.Class, sp.PII)
	}
	if op.DryRun {
		var err error
		res.More, err = s.dry(ctx, op, func(c context.Context) error {
			return s.l.r.QueryRowContext(c, `SELECT count(*), COALESCE(sum(length(data)),0) FROM records WHERE `+where, args...).Scan(&res.Rows, &res.BytesFreed)
		})
		return res, err
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT seq, data FROM records WHERE `+where+` ORDER BY seq LIMIT ?`, append(args, limitOf(op)+1)...)
		if err != nil {
			return err
		}
		type rec struct {
			seq  int64
			data []byte
		}
		var picked []rec
		for rows.Next() {
			var r rec
			if err := rows.Scan(&r.seq, &r.data); err != nil {
				_ = rows.Close()
				return err
			}
			picked = append(picked, r)
		}
		_ = rows.Close()
		if len(picked) > limitOf(op) {
			picked, res.More = picked[:limitOf(op)], true
		}
		for _, r := range picked {
			digest := "withheld"
			if !sp.Withhold {
				sum := sha256.Sum256(r.data)
				digest = "sha256:" + hex.EncodeToString(sum[:])
			}
			if _, err := tx.ExecContext(ctx, `UPDATE records SET data=NULL, tier=1, payload_digest=? WHERE stream=? AND seq=?`, digest, stream, r.seq); err != nil {
				return err
			}
			res.Rows++
			res.BytesFreed += int64(len(r.data))
		}
		return nil
	})
	return res, err
}

// Trim removes the contiguous prefix of eligible rows after pruned-through.
func (s *retentionStore) Trim(ctx context.Context, stream string, specs []retention.TrimSpec, op retention.Op) (retention.Result, error) {
	res := retention.Result{ByClass: map[string]int64{}}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	// R-audit rows are never removed, even when a caller names the class.
	kept := specs[:0:0]
	for _, sp := range specs {
		if sp.Class != retention.ClassAudit {
			kept = append(kept, sp)
		}
	}
	specs = kept
	if len(specs) == 0 {
		return res, nil
	}
	by := map[string]retention.TrimSpec{}
	for _, sp := range specs {
		by[sp.Class] = sp
	}
	eligibleSQL := make([]string, 0, len(specs))
	var eargs []any
	for _, sp := range specs {
		// A row is removed only once a covering rollup counted it (the rolled flag).
		eligibleSQL = append(eligibleSQL, `(class=? AND at_ns <= ? AND rolled=1)`)
		eargs = append(eargs, sp.Class, sp.Before.UnixNano())
	}
	anyEligible := strings.Join(eligibleSQL, " OR ")

	run := func(q interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}, write func(cut int64, n int64) error) error {
		var pruned int64
		if err := q.QueryRowContext(ctx, `SELECT pruned FROM streams WHERE stream=?`, stream).Scan(&pruned); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		limit := limitOf(op)
		if op.DryRun {
			limit = 1 << 30
		}
		rows, err := q.QueryContext(ctx, `SELECT seq, class, at_ns, rolled, size FROM records WHERE stream=? AND seq>? ORDER BY seq LIMIT ?`, stream, pruned, limit+1)
		if err != nil {
			return err
		}
		var cut, n int64
		stopped := false
		for rows.Next() {
			var seq, at, size int64
			var class string
			var rolled bool
			if err := rows.Scan(&seq, &class, &at, &rolled, &size); err != nil {
				_ = rows.Close()
				return err
			}
			sp, ok := by[class]
			if !ok || at > sp.Before.UnixNano() || !rolled {
				stopped = true
				break
			}
			if n >= int64(limit) {
				res.More = true
				break
			}
			cut, n = seq, n+1
			res.Rows++
			res.BytesFreed += size
			res.ByClass[class]++
		}
		_ = rows.Close()
		if stopped {
			// Rows past their envelope age that stay behind the row that stopped the prefix.
			if err := q.QueryRowContext(ctx, `SELECT count(*) FROM records WHERE stream=? AND seq>? AND (`+anyEligible+`)`,
				append([]any{stream, cut}, eargs...)...).Scan(&res.Held); err != nil {
				return err
			}
		}
		if n > 0 && !op.DryRun {
			return write(cut, n)
		}
		return nil
	}

	if op.DryRun {
		var err error
		res.More, err = s.dry(ctx, op, func(c context.Context) error { ctx = c; return run(s.l.r, nil) })
		return res, err
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		return run(tx, func(cut, n int64) error {
			del, err := tx.ExecContext(ctx, `DELETE FROM records WHERE stream=? AND seq<=?`, stream, cut)
			if err != nil {
				return err
			}
			removed, _ := del.RowsAffected()
			_, err = tx.ExecContext(ctx, `UPDATE streams SET pruned=?, dropped=dropped+? WHERE stream=?`, cut, removed, stream)
			return err
		})
	})
	if err == nil && res.Rows > 0 {
		_ = s.l.flushMarker(true)
	}
	return res, err
}

// Watermarks implements [retention.Store]: a head-only store has none; its
// pruned-through cursor plays that part.
func (s *retentionStore) Watermarks(ctx context.Context, _ string) (map[string]uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]uint64{}, nil
}

func (s *retentionStore) Prune(ctx context.Context, stream string, sp retention.PruneSpec, op retention.Op) (retention.Result, error) {
	var res retention.Result
	where := `stream=? AND class=? AND bucket_ns=? AND bucket_start + bucket_ns <= ?`
	args := []any{stream, sp.Class, int64(sp.Bucket), sp.Before.UnixNano()}
	if op.DryRun {
		var err error
		res.More, err = s.dry(ctx, op, func(c context.Context) error {
			return s.l.r.QueryRowContext(c, `SELECT count(*) FROM rollups WHERE `+where, args...).Scan(&res.Rows)
		})
		return res, err
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `DELETE FROM rollups WHERE `+where, args...)
		if err != nil {
			return err
		}
		res.Rows, _ = r.RowsAffected()
		return nil
	})
	return res, err
}
