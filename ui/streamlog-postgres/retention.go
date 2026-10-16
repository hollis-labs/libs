package streamlogpostgres

import (
	"context"
	"errors"
	"hash/fnv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention"
)

// RetentionStore returns the log as a [retention.Store], so a
// [retention.Janitor] can tombstone, roll up, trim and prune it. Trim is
// head-only: it advances the stream's pruned-through cursor exactly like
// [Log.Trim], so readers behind it get an ordinary retention gap. The janitor
// lock is a PostgreSQL advisory lock, so several instances share one janitor.
func (l *Log) RetentionStore() retention.Store { return &retentionStore{l: l} }

type retentionStore struct{ l *Log }

var _ retention.Store = (*retentionStore)(nil)

func (s *retentionStore) Caps() retention.Caps { return retention.Caps{Trim: retention.TrimHeadOnly} }

func (s *retentionStore) lockKey() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("streamlog-janitor:" + s.l.opts.Schema))
	return int64(h.Sum64())
}

// TryLock implements [retention.Store] with a session-level advisory lock held
// on one pooled connection until unlock.
func (s *retentionStore) TryLock(ctx context.Context) (func(), bool, error) {
	conn, err := s.l.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, s.lockKey()).Scan(&ok); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !ok {
		conn.Release()
		return nil, false, nil
	}
	return func() {
		// Unlock on a fresh context: the pass's own may be cancelled.
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(c, `SELECT pg_advisory_unlock($1)`, s.lockKey())
		conn.Release()
	}, true, nil
}

func (s *retentionStore) Streams(ctx context.Context) ([]string, error) {
	if err := s.l.enter(); err != nil {
		return nil, err
	}
	defer s.l.leave()
	rows, err := s.l.pool.Query(ctx, `SELECT stream FROM `+s.l.schema+`.streams UNION SELECT stream FROM `+s.l.schema+`.rollups ORDER BY 1`)
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
	rows, err := s.l.pool.Query(ctx, `SELECT class, count(*), min(at) FROM `+s.l.schema+`.records WHERE stream=$1 GROUP BY class ORDER BY class`, stream)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []retention.ClassStat
	for rows.Next() {
		var c retention.ClassStat
		var oldest time.Time
		if err := rows.Scan(&c.Class, &c.Rows, &oldest); err != nil {
			return nil, err
		}
		c.Oldest = oldest.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *retentionStore) tx(ctx context.Context, f func(pgx.Tx) error) error {
	if err := s.l.enter(); err != nil {
		return err
	}
	defer s.l.leave()
	tx, err := s.l.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// dry runs a counting query under the call's MaxDuration. A count that cannot
// finish in time is not an error: it reports More.
func (s *retentionStore) dry(ctx context.Context, op retention.Op, f func(context.Context) error) (more bool, err error) {
	if err := s.l.enter(); err != nil {
		return false, err
	}
	defer s.l.leave()
	c := ctx
	if op.MaxDuration > 0 {
		var cancel context.CancelFunc
		c, cancel = context.WithTimeout(ctx, op.MaxDuration)
		defer cancel()
	}
	if err := f(c); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || c.Err() == context.DeadlineExceeded {
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
	t := s.l.schema + ".records"
	if op.DryRun {
		var err error
		res.More, err = s.dry(ctx, op, func(c context.Context) error {
			return s.l.pool.QueryRow(c, `SELECT count(*) FROM `+t+` WHERE stream=$1 AND class=''`, stream).Scan(&res.Rows)
		})
		return res, err
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT category, severity, sensitivity, pii FROM `+t+` WHERE stream=$1 AND class=''`, stream)
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
				rows.Close()
				return err
			}
			combos = append(combos, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		budget := limitOf(op)
		for _, c := range combos {
			if budget <= 0 {
				break
			}
			class := classOf(retention.Attrs{Category: c.cat, Severity: c.sev, Sensitivity: c.sens, PII: c.pii})
			tag, err := tx.Exec(ctx, `UPDATE `+t+` SET class=$1 WHERE (stream, seq) IN
				(SELECT stream, seq FROM `+t+` WHERE stream=$2 AND class='' AND category=$3 AND severity=$4 AND sensitivity=$5 AND pii=$6 LIMIT $7)`,
				class, stream, c.cat, c.sev, c.sens, c.pii, budget)
			if err != nil {
				return err
			}
			res.Rows += tag.RowsAffected()
			budget -= int(tag.RowsAffected())
		}
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+t+` WHERE stream=$1 AND class='')`, stream).Scan(&res.More)
	})
	return res, err
}

func closedBound(largest retention.Duration, closedBefore time.Time) time.Time {
	n := int64(largest)
	c := closedBefore.UnixNano()
	q := c / n
	if c < 0 && c%n != 0 {
		q--
	}
	return time.Unix(0, q*n).UTC()
}

func bucketFloor(at time.Time, size retention.Duration) time.Time {
	n := int64(size)
	u := at.UnixNano()
	q := u / n
	if u < 0 && u%n != 0 {
		q--
	}
	return time.Unix(0, q*n).UTC()
}

type rollKey struct {
	bucketNS                                                      int64
	start                                                         time.Time
	kind, category, severity, project, workstream, agent, outcome string
}

type rollVal struct{ count, first, last int64 }

func (s *retentionStore) Rollup(ctx context.Context, stream string, sp retention.RollupSpec, op retention.Op) (retention.RollupResult, error) {
	var res retention.RollupResult
	if len(sp.Buckets) == 0 {
		return res, nil
	}
	t, rt := s.l.schema+".records", s.l.schema+".rollups"
	bound := closedBound(sp.Buckets[len(sp.Buckets)-1], sp.ClosedBefore)
	const where = `stream=$1 AND NOT rolled AND class=$2 AND at < $3`
	if op.DryRun {
		var err error
		res.More, err = s.dry(ctx, op, func(c context.Context) error {
			return s.l.pool.QueryRow(c, `SELECT count(*) FROM `+t+` WHERE `+where, stream, sp.Class, bound).Scan(&res.Rows)
		})
		return res, err
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT seq, at, kind, category, severity, project_id, workstream_id, agent_urn, outcome
			FROM `+t+` WHERE `+where+` ORDER BY seq LIMIT $4 FOR UPDATE`, stream, sp.Class, bound, limitOf(op)+1)
		if err != nil {
			return err
		}
		type rec struct {
			seq                                                           int64
			at                                                            time.Time
			kind, category, severity, project, workstream, agent, outcome string
		}
		var picked []rec
		for rows.Next() {
			var r rec
			if err := rows.Scan(&r.seq, &r.at, &r.kind, &r.category, &r.severity, &r.project, &r.workstream, &r.agent, &r.outcome); err != nil {
				rows.Close()
				return err
			}
			picked = append(picked, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(picked) > limitOf(op) {
			picked, res.More = picked[:limitOf(op)], true
		}
		agg := map[rollKey]*rollVal{}
		for _, r := range picked {
			for _, size := range sp.Buckets {
				k := rollKey{bucketNS: int64(size), start: bucketFloor(r.at, size), kind: r.kind, category: r.category, severity: r.severity,
					project: r.project, workstream: r.workstream, agent: r.agent, outcome: r.outcome}
				if v := agg[k]; v == nil {
					agg[k] = &rollVal{count: 1, first: r.seq, last: r.seq}
				} else {
					v.count++
					v.first, v.last = min(v.first, r.seq), max(v.last, r.seq)
				}
			}
		}
		for k, v := range agg {
			if _, err := tx.Exec(ctx, `INSERT INTO `+rt+`(stream, class, bucket_ns, bucket_start, kind, category, severity, project_id, workstream_id, agent_urn, outcome, count, first_seq, last_seq)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
				ON CONFLICT (stream, class, bucket_ns, bucket_start, kind, category, severity, project_id, workstream_id, agent_urn, outcome)
				DO UPDATE SET count = `+rt+`.count + excluded.count, first_seq = LEAST(`+rt+`.first_seq, excluded.first_seq), last_seq = GREATEST(`+rt+`.last_seq, excluded.last_seq)`,
				stream, sp.Class, k.bucketNS, k.start, k.kind, k.category, k.severity, k.project, k.workstream, k.agent, k.outcome, v.count, v.first, v.last); err != nil {
				return err
			}
			res.Buckets++
		}
		if len(picked) > 0 {
			seqs := make([]int64, len(picked))
			for i, r := range picked {
				seqs[i] = r.seq
			}
			if _, err := tx.Exec(ctx, `UPDATE `+t+` SET rolled=true WHERE stream=$1 AND seq = ANY($2)`, stream, seqs); err != nil {
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
	t := s.l.schema + ".records"
	where := `stream=$1 AND tier=0 AND at <= $2`
	args := []any{stream, sp.Before.UTC()}
	if sp.Secret {
		where += ` AND sensitivity='secret'`
	} else {
		where += ` AND class=$3 AND pii=$4`
		args = append(args, sp.Class, sp.PII)
	}
	if op.DryRun {
		var err error
		res.More, err = s.dry(ctx, op, func(c context.Context) error {
			return s.l.pool.QueryRow(c, `SELECT count(*), COALESCE(sum(length(data)),0) FROM `+t+` WHERE `+where, args...).Scan(&res.Rows, &res.BytesFreed)
		})
		return res, err
	}
	digest := `'sha256:' || encode(sha256(COALESCE(r.data, ''::bytea)), 'hex')`
	if sp.Withhold {
		digest = `'withheld'`
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		q := `WITH t AS (SELECT stream, seq, COALESCE(length(data),0) AS n FROM ` + t + ` WHERE ` + where +
			` ORDER BY seq LIMIT $` + itoa(len(args)+1) + ` FOR UPDATE),
		u AS (UPDATE ` + t + ` r SET data=NULL, payload=NULL, tier=1, payload_digest=` + digest + `
			FROM t WHERE r.stream=t.stream AND r.seq=t.seq RETURNING t.n)
		SELECT count(*), COALESCE(sum(n),0) FROM u`
		if err := tx.QueryRow(ctx, q, append(args, limitOf(op))...).Scan(&res.Rows, &res.BytesFreed); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+t+` WHERE `+where+`)`, args...).Scan(&res.More)
	})
	return res, err
}

func itoa(n int) string {
	const digits = "0123456789"
	if n < 10 {
		return digits[n : n+1]
	}
	return itoa(n/10) + digits[n%10:n%10+1]
}

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
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
	t, st := s.l.schema+".records", s.l.schema+".streams"
	by := map[string]retention.TrimSpec{}
	var conds []string
	var eargs []any
	for i, sp := range specs {
		by[sp.Class] = sp
		c := `(class=$` + itoa(3+2*i) + ` AND at <= $` + itoa(4+2*i)
		if sp.RequireRolled {
			c += ` AND rolled`
		}
		conds = append(conds, c+`)`)
		eargs = append(eargs, sp.Class, sp.Before.UTC())
	}
	anyEligible := strings.Join(conds, " OR ")

	// scan walks the rows after pruned-through, filling res, and returns the
	// cursor of the last eligible row of the prefix (0 if none).
	scan := func(ctx context.Context, q querier, lock bool) (int64, error) {
		var pruned int64
		sel := `SELECT pruned FROM ` + st + ` WHERE stream=$1`
		if lock {
			sel += ` FOR UPDATE`
		}
		if err := q.QueryRow(ctx, sel, stream).Scan(&pruned); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, nil
			}
			return 0, err
		}
		limit := limitOf(op)
		if op.DryRun {
			limit = 1 << 30
		}
		rows, err := q.Query(ctx, `SELECT seq, class, at, rolled, size FROM `+t+` WHERE stream=$1 AND seq>$2 ORDER BY seq LIMIT $3`, stream, pruned, limit+1)
		if err != nil {
			return 0, err
		}
		var cut, n int64
		stopped := false
		for rows.Next() {
			var seq, size int64
			var class string
			var at time.Time
			var rolled bool
			if err := rows.Scan(&seq, &class, &at, &rolled, &size); err != nil {
				rows.Close()
				return 0, err
			}
			sp, ok := by[class]
			if !ok || at.After(sp.Before) || (sp.RequireRolled && !rolled) {
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
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
		if stopped {
			// Rows past their envelope age that stay behind the row that stopped the prefix.
			if err := q.QueryRow(ctx, `SELECT count(*) FROM `+t+` WHERE stream=$1 AND seq>$2 AND (`+anyEligible+`)`,
				append([]any{stream, cut}, eargs...)...).Scan(&res.Held); err != nil {
				return 0, err
			}
		}
		return cut, nil
	}

	if op.DryRun {
		more, err := s.dry(ctx, op, func(c context.Context) error {
			_, err := scan(c, s.l.pool, false)
			return err
		})
		res.More = res.More || more
		return res, err
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		cut, err := scan(ctx, tx, true)
		if err != nil || res.Rows == 0 {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM `+t+` WHERE stream=$1 AND seq<=$2`, stream, cut)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE `+st+` SET pruned=$1, dropped=dropped+$2 WHERE stream=$3`, cut, tag.RowsAffected(), stream)
		return err
	})
	if err == nil && res.Rows > 0 {
		_ = s.l.flushMarker(true)
	}
	return res, err
}

// Watermarks implements [retention.Store]: a head-only store has none; its
// pruned cursor plays that part.
func (s *retentionStore) Watermarks(ctx context.Context, _ string) (map[string]uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]uint64{}, nil
}

func (s *retentionStore) Prune(ctx context.Context, stream string, sp retention.PruneSpec, op retention.Op) (retention.Result, error) {
	var res retention.Result
	rt := s.l.schema + ".rollups"
	where := `stream=$1 AND class=$2 AND bucket_ns=$3 AND bucket_start + (bucket_ns / 1000) * interval '1 microsecond' <= $4`
	args := []any{stream, sp.Class, int64(sp.Bucket), sp.Before.UTC()}
	if op.DryRun {
		var err error
		res.More, err = s.dry(ctx, op, func(c context.Context) error {
			return s.l.pool.QueryRow(c, `SELECT count(*) FROM `+rt+` WHERE `+where, args...).Scan(&res.Rows)
		})
		return res, err
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM `+rt+` WHERE `+where, args...)
		res.Rows = tag.RowsAffected()
		return err
	})
	return res, err
}
