package streamlogpostgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/logkit"
)

const (
	defaultSchema         = "streamlog"
	defaultPageSize       = 128
	defaultMarkerInterval = time.Second
	uniqueViolation       = "23505"
)

// ErrStorageMismatch is returned by [Open] under [RefuseOnMismatch].
var ErrStorageMismatch = errors.New("streamlogpostgres: database does not match its recorded identity")

// MismatchPolicy says what Open does when a restore check fails.
type MismatchPolicy int

const (
	// RemintOnMismatch (the default) assigns a new epoch.
	RemintOnMismatch MismatchPolicy = iota
	// RefuseOnMismatch makes Open fail with [ErrStorageMismatch].
	RefuseOnMismatch
)

// Options configures [Open]. The zero value is usable.
type Options struct {
	// Schema holds the log's tables (default "streamlog").
	Schema string
	// Extractor fills the indexed columns; default [logkit.EnvelopeExtractor].
	Extractor logkit.Extractor
	// PageSize is how many records After reads per page (default 128).
	PageSize int
	// Now is the clock for Record.At and MaxAge trimming (default time.Now).
	Now func() time.Time
	// MarkerPath is the external high-water marker file. Default: a file under
	// the user config directory named after the database and schema.
	MarkerPath string
	// DisableMarker turns the marker off. Open then warns: a pg_restore into
	// the same cluster, or a snapshot rollback, would go undetected.
	DisableMarker bool
	// MarkerEvery throttles marker writes after appends (default 1s; negative
	// writes after every append). Open, Trim and Close always flush.
	MarkerEvery time.Duration
	// OnStorageMismatch is the restore policy.
	OnStorageMismatch MismatchPolicy
	// OnIDConflict is called when an Event.ID is appended again with
	// different content, after the counter is bumped. Wire it to an alert.
	OnIDConflict func(stream string, e streamhub.Event)
	// Warn receives startup warnings; default slog.Warn.
	Warn func(msg string)
}

// Recovery reports what Open found.
type Recovery struct {
	Reminted      bool
	PreviousEpoch string
	// Reason is one of "epoch_differs", "head_behind_marker", "marker_unreadable",
	// "system_identifier_changed", "timeline_changed".
	Reason string
	// Warnings are conditions that weaken restore detection.
	Warnings []string
}

// Stats are monotonic counters since Open.
type Stats struct{ IDConflicts uint64 }

// Log is a durable [streamhub.Log] on PostgreSQL.
type Log struct {
	pool    *pgxpool.Pool
	ownPool bool
	schema  string // quoted
	opts    Options
	page    int
	now     func() time.Time
	extract logkit.Extractor

	markerPath string
	markerGap  time.Duration

	epoch    string
	recovery Recovery

	conflicts atomic.Uint64

	mu     sync.RWMutex
	closed bool

	mkMu    sync.Mutex
	heads   map[string]uint64
	dirty   bool
	lastMk  time.Time
	mkError error
}

var (
	_ streamhub.Log         = (*Log)(nil)
	_ streamhub.EpochReader = (*Log)(nil)
)

// OpenDSN connects with a libpq-style URL or DSN and opens the log. The log
// owns the pool and closes it.
func OpenDSN(ctx context.Context, dsn string, o Options) (*Log, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("streamlogpostgres: connect: %w", err)
	}
	l, err := Open(ctx, pool, o)
	if err != nil {
		pool.Close()
		return nil, err
	}
	l.ownPool = true
	return l, nil
}

// Open opens or creates the log in pool. The caller keeps ownership of pool.
func Open(ctx context.Context, pool *pgxpool.Pool, o Options) (*Log, error) {
	if o.Schema == "" {
		o.Schema = defaultSchema
	}
	l := &Log{
		pool: pool, opts: o, schema: pgx.Identifier{o.Schema}.Sanitize(), page: o.PageSize,
		now: o.Now, extract: o.Extractor, markerGap: o.MarkerEvery, heads: map[string]uint64{},
	}
	if l.page <= 0 {
		l.page = defaultPageSize
	}
	if l.now == nil {
		l.now = time.Now
	}
	if l.extract == nil {
		l.extract = logkit.EnvelopeExtractor
	}
	if l.markerGap == 0 {
		l.markerGap = defaultMarkerInterval
	}
	if err := l.init(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

func mintEpoch() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (l *Log) warn(msg string) {
	l.recovery.Warnings = append(l.recovery.Warnings, msg)
	if l.opts.Warn != nil {
		l.opts.Warn(msg)
		return
	}
	slog.Warn("streamlogpostgres: " + msg)
}

// serverIdentity reads the server's system identifier and timeline. It runs
// outside the init transaction because a permission error would abort it.
func (l *Log) serverIdentity(ctx context.Context) (sysid, timeline string, err error) {
	if err = l.pool.QueryRow(ctx, `SELECT system_identifier::text FROM pg_control_system()`).Scan(&sysid); err != nil {
		return "", "", err
	}
	if err = l.pool.QueryRow(ctx, `SELECT timeline_id::text FROM pg_control_checkpoint()`).Scan(&timeline); err != nil {
		return "", "", err
	}
	return sysid, timeline, nil
}

func (l *Log) resolveMarkerPath() string {
	if l.opts.MarkerPath != "" {
		return l.opts.MarkerPath
	}
	cc := l.pool.Config().ConnConfig
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d/%s/%s", cc.Host, cc.Port, cc.Database, l.opts.Schema)))
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
		l.warn("no user config directory; the restore marker is in the temp directory and may not survive a reboot (set Options.MarkerPath)")
	}
	return filepath.Join(dir, "hollis", "streamlog", hex.EncodeToString(sum[:8])+".hwm")
}

func (l *Log) init(ctx context.Context) error {
	sysid, timeline, idErr := l.serverIdentity(ctx)
	if idErr != nil {
		l.warn("server system identifier and timeline are unavailable (" + idErr.Error() +
			"); cluster-swap and point-in-time-recovery detection is off, only the marker applies")
	}
	if l.opts.DisableMarker {
		l.warn("the external high-water marker is disabled: a pg_restore into the same cluster or a snapshot rollback will not be detected")
	} else {
		l.markerPath = l.resolveMarkerPath()
	}

	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("streamlogpostgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize concurrent Opens on the same schema while it is created.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "streamlog:"+l.opts.Schema); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(schemaSQL, l.schema)); err != nil {
		return fmt.Errorf("streamlogpostgres: schema: %w", err)
	}

	meta := func(key string) (string, bool, error) {
		var v string
		err := tx.QueryRow(ctx, `SELECT value FROM `+l.schema+`.log_meta WHERE key=$1`, key).Scan(&v)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return v, err == nil, err
	}
	setMeta := func(key, value string) error {
		_, err := tx.Exec(ctx, `INSERT INTO `+l.schema+`.log_meta(key,value) VALUES($1,$2)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
		return err
	}

	ver, ok, err := meta("schema_version")
	if err != nil {
		return err
	}
	if !ok {
		if err := setMeta("schema_version", schemaVersion); err != nil {
			return err
		}
	} else if ver != schemaVersion {
		return fmt.Errorf("streamlogpostgres: schema version %q, this build understands %q", ver, schemaVersion)
	}

	rows, err := tx.Query(ctx, `SELECT stream, latest FROM `+l.schema+`.streams`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var s string
		var n int64
		if err := rows.Scan(&s, &n); err != nil {
			return err
		}
		l.heads[s] = uint64(n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	storedEpoch, havEpoch, err := meta("epoch")
	if err != nil {
		return err
	}
	l.epoch = storedEpoch
	fresh := !havEpoch

	reason := ""
	prev := storedEpoch
	// 1. The server identity recorded when the epoch was minted.
	if !fresh && idErr == nil {
		if v, ok, err := meta("pg_system_identifier"); err != nil {
			return err
		} else if ok && v != sysid {
			reason = "system_identifier_changed"
		}
		if v, ok, err := meta("pg_timeline"); err != nil {
			return err
		} else if reason == "" && ok && v != timeline {
			reason = "timeline_changed"
		}
	}
	// 2. The external marker.
	if !l.opts.DisableMarker {
		mk, merr := logkit.ReadMarker(l.markerPath)
		switch {
		case merr != nil && !errors.Is(merr, logkit.ErrMarkerUnreadable):
			return fmt.Errorf("streamlogpostgres: read marker: %w", merr)
		case errors.Is(merr, logkit.ErrMarkerUnreadable):
			if reason == "" && !fresh {
				reason = "marker_unreadable"
			}
		case mk != nil && fresh && mk.Epoch != "":
			if reason == "" {
				reason, prev = "epoch_differs", mk.Epoch
			}
		case mk != nil && !fresh && mk.Epoch != l.epoch:
			if reason == "" {
				reason = "epoch_differs"
			}
		case mk != nil && !fresh:
			if _, behind := mk.Behind(l.heads); behind && reason == "" {
				reason = "head_behind_marker"
			}
		}
	}
	if reason != "" && l.opts.OnStorageMismatch == RefuseOnMismatch {
		return fmt.Errorf("%w (%s)", ErrStorageMismatch, reason)
	}
	if fresh || reason != "" {
		if l.epoch, err = mintEpoch(); err != nil {
			return err
		}
		if err := setMeta("epoch", l.epoch); err != nil {
			return err
		}
		if reason != "" {
			l.recovery.Reminted, l.recovery.PreviousEpoch, l.recovery.Reason = true, prev, reason
		}
	}
	if idErr == nil && (fresh || reason != "") {
		if err := setMeta("pg_system_identifier", sysid); err != nil {
			return err
		}
		if err := setMeta("pg_timeline", timeline); err != nil {
			return err
		}
	} else if idErr == nil {
		// Existing log written before identities were recorded: adopt them.
		if _, ok, _ := meta("pg_system_identifier"); !ok {
			if err := setMeta("pg_system_identifier", sysid); err != nil {
				return err
			}
			if err := setMeta("pg_timeline", timeline); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	l.dirty = true
	return l.flushMarker(true)
}

// Epoch is the identity of this log; see the package documentation.
func (l *Log) Epoch() string { return l.epoch }

// Recovery reports whether Open re-minted the epoch, why, and any warnings.
func (l *Log) Recovery() Recovery { return l.recovery }

// Stats returns counters since Open.
func (l *Log) Stats() Stats { return Stats{IDConflicts: l.conflicts.Load()} }

// MarkerError returns the last error from writing the marker file, if any.
func (l *Log) MarkerError() error {
	l.mkMu.Lock()
	defer l.mkMu.Unlock()
	return l.mkError
}

func (l *Log) noteHead(stream string, seq uint64) {
	l.mkMu.Lock()
	if seq > l.heads[stream] {
		l.heads[stream] = seq
	}
	l.dirty = true
	l.mkMu.Unlock()
	_ = l.flushMarker(false)
}

func (l *Log) flushMarker(force bool) error {
	if l.opts.DisableMarker {
		return nil
	}
	l.mkMu.Lock()
	defer l.mkMu.Unlock()
	if !l.dirty {
		return nil
	}
	if !force && l.markerGap > 0 && l.now().Sub(l.lastMk) < l.markerGap {
		return nil
	}
	m := logkit.Marker{Epoch: l.epoch, Streams: make(map[string]uint64, len(l.heads))}
	for s, n := range l.heads {
		m.Streams[s] = n
	}
	if err := logkit.WriteMarker(l.markerPath, m); err != nil {
		l.mkError = err
		if force {
			return fmt.Errorf("streamlogpostgres: write marker: %w", err)
		}
		return err
	}
	l.mkError, l.dirty, l.lastMk = nil, false, l.now()
	return nil
}

func copyBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return append([]byte(nil), b...)
}

// jsonPayload returns the data as a JSONB-safe string, or nil when it is not
// valid JSON (or holds a NUL escape, which JSONB rejects).
func jsonPayload(b []byte) any {
	if len(b) == 0 || !json.Valid(b) || bytes.Contains(b, []byte(`\u0000`)) {
		return nil
	}
	return string(b)
}

func (l *Log) enter() error {
	l.mu.RLock()
	if l.closed {
		l.mu.RUnlock()
		return streamhub.ErrClosed
	}
	return nil
}

func (l *Log) leave() { l.mu.RUnlock() }

// Append implements [streamhub.Log].
func (l *Log) Append(ctx context.Context, stream string, e streamhub.Event) (streamhub.Record, error) {
	if err := ctx.Err(); err != nil {
		return streamhub.Record{}, err
	}
	if err := l.enter(); err != nil {
		return streamhub.Record{}, err
	}
	defer l.leave()

	data := copyBytes(e.Data)
	digest := logkit.Digest(e)
	cols := l.extract(e)
	at := l.now().UTC().Truncate(time.Microsecond)
	var eid any
	if e.ID != "" {
		eid = e.ID
	}

	// A concurrent append of the same id can lose the unique index race; the
	// second attempt then finds the winner and treats it as a retry.
	for attempt := 0; ; attempt++ {
		rec, retry, err := l.appendOnce(ctx, stream, e, eid, data, digest, cols, at)
		if retry && attempt < 3 {
			continue
		}
		return rec, err
	}
}

func (l *Log) appendOnce(ctx context.Context, stream string, e streamhub.Event, eid any, data, digest []byte,
	cols logkit.Columns, at time.Time) (streamhub.Record, bool, error) {
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return streamhub.Record{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if e.ID != "" {
		var (
			ostream, okind string
			oseq           int64
			odigest, odata []byte
			oat            time.Time
		)
		err := tx.QueryRow(ctx, `SELECT stream, seq, kind, digest, at, data FROM `+l.schema+`.records WHERE event_id=$1`, e.ID).
			Scan(&ostream, &oseq, &okind, &odigest, &oat, &odata)
		switch {
		case err == nil:
			if ostream == stream && bytes.Equal(odigest, digest) {
				return streamhub.Record{Seq: streamhub.Seq(oseq), Name: okind, Data: odata, At: oat.UTC(), ID: e.ID, Epoch: l.epoch}, false, nil
			}
			l.conflicts.Add(1)
			if l.opts.OnIDConflict != nil {
				l.opts.OnIDConflict(stream, e)
			}
			return streamhub.Record{}, false, streamhub.ErrEventIDConflict
		case !errors.Is(err, pgx.ErrNoRows):
			return streamhub.Record{}, false, err
		}
	}

	var seq int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO `+l.schema+`.streams(stream, latest) VALUES($1, 1)
		 ON CONFLICT (stream) DO UPDATE SET latest = `+l.schema+`.streams.latest + 1 RETURNING latest`, stream).Scan(&seq); err != nil {
		return streamhub.Record{}, false, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO `+l.schema+`.records(stream, seq, event_id, kind, at, size, data, payload, digest,
			category, severity, sensitivity, pii, visibility, generation,
			project_id, workstream_id, task_id, session_id, agent_urn, run_id)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9, $10,$11,$12,$13,$14,$15, $16,$17,$18,$19,$20,$21)`,
		stream, seq, eid, e.Name, at, len(e.Name)+len(data), data, jsonPayload(data), digest,
		cols.Category, cols.Severity, cols.Sensitivity, cols.PII, cols.Visibility, cols.Generation,
		cols.ProjectID, cols.WorkstreamID, cols.TaskID, cols.SessionID, cols.AgentURN, cols.RunID); err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == uniqueViolation && e.ID != "" {
			return streamhub.Record{}, true, err
		}
		return streamhub.Record{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return streamhub.Record{}, false, err
	}
	l.noteHead(stream, uint64(seq))
	return streamhub.Record{Seq: streamhub.Seq(seq), Name: e.Name, Data: data, At: at, ID: e.ID, Epoch: l.epoch}, false, nil
}

// After implements [streamhub.Log].
func (l *Log) After(ctx context.Context, stream string, after streamhub.Seq) iter.Seq2[streamhub.Record, error] {
	return l.AfterEpoch(ctx, stream, "", after)
}

// AfterCursor is After for an opaque cursor made by [logkit.FormatCursor].
func (l *Log) AfterCursor(ctx context.Context, stream, cursor string) iter.Seq2[streamhub.Record, error] {
	epoch, seq, err := logkit.ParseCursor(cursor)
	if err != nil {
		return func(yield func(streamhub.Record, error) bool) { yield(streamhub.Record{}, err) }
	}
	return l.AfterEpoch(ctx, stream, epoch, seq)
}

// AfterEpoch implements [streamhub.EpochReader].
func (l *Log) AfterEpoch(ctx context.Context, stream, epoch string, after streamhub.Seq) iter.Seq2[streamhub.Record, error] {
	return func(yield func(streamhub.Record, error) bool) {
		cursor := after
		for {
			recs, err := l.readPage(ctx, stream, epoch, cursor)
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

func (l *Log) readPage(ctx context.Context, stream, epoch string, cursor streamhub.Seq) ([]streamhub.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := l.enter(); err != nil {
		return nil, err
	}
	defer l.leave()

	tx, err := l.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var latest, pruned int64
	if err := tx.QueryRow(ctx, `SELECT latest, pruned FROM `+l.schema+`.streams WHERE stream=$1`, stream).Scan(&latest, &pruned); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	gap := func(reason streamhub.GapReason, missed uint64) error {
		return &streamhub.GapError{Gap: streamhub.Gap{
			Reason: reason, Requested: cursor, OldestAvailable: streamhub.Seq(pruned + 1),
			Latest: streamhub.Seq(latest), Missed: missed, Epoch: l.epoch,
		}}
	}
	switch {
	case epoch != "" && epoch != l.epoch:
		return nil, gap(streamhub.GapEpochChanged, 0)
	case uint64(cursor) > uint64(latest):
		return nil, gap(streamhub.GapCursorAhead, uint64(cursor)-uint64(latest))
	case uint64(cursor) < uint64(pruned):
		return nil, gap(streamhub.GapRetention, uint64(pruned)-uint64(cursor))
	}
	rows, err := tx.Query(ctx,
		`SELECT seq, kind, at, data, event_id FROM `+l.schema+`.records WHERE stream=$1 AND seq>$2 ORDER BY seq LIMIT $3`,
		stream, int64(cursor), l.page)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []streamhub.Record
	for rows.Next() {
		var (
			seq int64
			r   streamhub.Record
			at  time.Time
			eid *string
		)
		if err := rows.Scan(&seq, &r.Name, &at, &r.Data, &eid); err != nil {
			return nil, err
		}
		r.Seq, r.At, r.Epoch = streamhub.Seq(seq), at.UTC(), l.epoch
		if eid != nil {
			r.ID = *eid
		}
		if len(r.Data) == 0 {
			r.Data = nil
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Head implements [streamhub.Log].
func (l *Log) Head(ctx context.Context, stream string) (streamhub.Head, error) {
	if err := ctx.Err(); err != nil {
		return streamhub.Head{}, err
	}
	if err := l.enter(); err != nil {
		return streamhub.Head{}, err
	}
	defer l.leave()
	h := streamhub.Head{Epoch: l.epoch}
	var latest, pruned, dropped int64
	err := l.pool.QueryRow(ctx, `SELECT latest, pruned, dropped FROM `+l.schema+`.streams WHERE stream=$1`, stream).Scan(&latest, &pruned, &dropped)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, nil
	}
	if err != nil {
		return streamhub.Head{}, err
	}
	h.Latest, h.PrunedThrough, h.Dropped = streamhub.Seq(latest), streamhub.Seq(pruned), uint64(dropped)
	return h, nil
}

// Trim implements [streamhub.Log]. Records leave from the head only.
func (l *Log) Trim(ctx context.Context, stream string, r streamhub.Retention) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := l.enter(); err != nil {
		return err
	}
	defer l.leave()

	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var latest, pruned int64
	if err := tx.QueryRow(ctx, `SELECT latest, pruned FROM `+l.schema+`.streams WHERE stream=$1 FOR UPDATE`, stream).Scan(&latest, &pruned); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	cut := pruned
	if r.MaxRecords > 0 && latest-pruned > int64(r.MaxRecords) {
		cut = max(cut, latest-int64(r.MaxRecords))
	}
	if r.MaxAge > 0 {
		var c *int64
		thr := l.now().Add(-r.MaxAge).UTC()
		if err := tx.QueryRow(ctx, `SELECT MAX(seq) FROM `+l.schema+`.records WHERE stream=$1 AND seq>$2 AND at<$3`, stream, pruned, thr).Scan(&c); err != nil {
			return err
		}
		if c != nil {
			cut = max(cut, *c)
		}
	}
	if r.MaxBytes > 0 {
		var c *int64
		if err := tx.QueryRow(ctx,
			`SELECT MAX(seq) FROM (SELECT seq, SUM(size) OVER (ORDER BY seq DESC) AS tail
			 FROM `+l.schema+`.records WHERE stream=$1 AND seq>$2) t WHERE tail > $3`, stream, pruned, int64(r.MaxBytes)).Scan(&c); err != nil {
			return err
		}
		if c != nil {
			cut = max(cut, *c)
		}
	}
	if cut <= pruned {
		return nil
	}
	tag, err := tx.Exec(ctx, `DELETE FROM `+l.schema+`.records WHERE stream=$1 AND seq<=$2`, stream, cut)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE `+l.schema+`.streams SET pruned=$1, dropped=dropped+$2 WHERE stream=$3`, cut, tag.RowsAffected(), stream); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	_ = l.flushMarker(true)
	return nil
}

// Close implements [streamhub.Log]: it writes the marker and, if the log
// opened the pool ([OpenDSN]), closes it.
func (l *Log) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	err := l.flushMarker(true)
	if l.ownPool {
		l.pool.Close()
	}
	return err
}

// ColumnsOf returns the indexed columns stored for one record.
func (l *Log) ColumnsOf(ctx context.Context, stream string, seq streamhub.Seq) (logkit.Columns, error) {
	if err := l.enter(); err != nil {
		return logkit.Columns{}, err
	}
	defer l.leave()
	var c logkit.Columns
	err := l.pool.QueryRow(ctx,
		`SELECT category, severity, sensitivity, pii, visibility, generation, project_id, workstream_id, task_id, session_id, agent_urn, run_id
		 FROM `+l.schema+`.records WHERE stream=$1 AND seq=$2`, stream, int64(seq)).
		Scan(&c.Category, &c.Severity, &c.Sensitivity, &c.PII, &c.Visibility, &c.Generation, &c.ProjectID, &c.WorkstreamID, &c.TaskID, &c.SessionID, &c.AgentURN, &c.RunID)
	return c, err
}

// HasIndex reports whether the named index exists in the log's schema.
func (l *Log) HasIndex(ctx context.Context, name string) bool {
	var n int
	err := l.pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname=$1 AND indexname=$2`, l.opts.Schema, name).Scan(&n)
	return err == nil && n == 1
}

// Schema is the (unquoted) schema name this log uses.
func (l *Log) Schema() string { return l.opts.Schema }

// MarkerPath is the external marker file, empty when the marker is disabled.
func (l *Log) MarkerPath() string { return l.markerPath }
