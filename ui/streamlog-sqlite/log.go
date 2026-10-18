package streamlogsqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/libs/core/util/sqlite/sqlitekit"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/logkit"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

const (
	defaultPageSize       = 128
	defaultMarkerInterval = time.Second
)

// ErrStorageMismatch is returned by [Open] under [RefuseOnMismatch] when the
// marker file disagrees with the database.
var ErrStorageMismatch = errors.New("streamlogsqlite: database does not match its high-water marker")

// MismatchPolicy says what Open does when the database disagrees with the
// marker file kept outside it.
type MismatchPolicy int

const (
	// RemintOnMismatch (the default) assigns a new epoch, so every cursor
	// held under the old one is answered with an epoch_changed gap.
	RemintOnMismatch MismatchPolicy = iota
	// RefuseOnMismatch makes Open fail with [ErrStorageMismatch].
	RefuseOnMismatch
)

// Options configures [Open]. The zero value is usable.
type Options struct {
	// Extractor fills the indexed columns; default [logkit.EnvelopeExtractor].
	Extractor logkit.Extractor
	// PageSize is how many records After reads per page (default 128).
	PageSize int
	// Now is the clock for Record.At and MaxAge trimming (default time.Now).
	Now func() time.Time
	// MarkerPath is the high-water marker file; default Open's path + ".hwm".
	MarkerPath string
	// MarkerEvery throttles marker writes after appends (default 1s; a
	// negative value writes after every append). The marker is always written
	// by Open and Close. A stale marker only weakens rollback detection; it
	// never causes a false mismatch.
	MarkerEvery time.Duration
	// OnStorageMismatch is the restore policy; see [MismatchPolicy].
	OnStorageMismatch MismatchPolicy
	// OnIDConflict is called, after the counter is bumped, when an Event.ID
	// is appended again with different content. Wire it to an alert.
	OnIDConflict func(stream string, e streamhub.Event)
	// ReadConns bounds the read pool (default from sqlitekit).
	ReadConns int
}

// Recovery reports what Open found when it checked the marker.
type Recovery struct {
	// Reminted is true when Open assigned a new epoch to an existing database.
	Reminted bool
	// PreviousEpoch is the epoch that was replaced, when known.
	PreviousEpoch string
	// Reason says why: "epoch_differs", "head_behind_marker" or "marker_unreadable".
	Reason string
}

// Stats are monotonic counters since Open.
type Stats struct {
	// IDConflicts counts appends rejected with [streamhub.ErrEventIDConflict].
	IDConflicts uint64
}

// Log is a durable [streamhub.Log] backed by one SQLite file.
type Log struct {
	w, r       *sql.DB
	path       string
	markerPath string
	markerGap  time.Duration
	page       int
	now        func() time.Time
	extract    logkit.Extractor
	onConflict func(string, streamhub.Event)

	epoch    string
	recovery Recovery

	conflicts atomic.Uint64

	mu     sync.RWMutex // guards closed vs. in-flight calls
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

// Open opens or creates the log at path (a filesystem path, not a URI).
func Open(ctx context.Context, path string, o Options) (*Log, error) {
	if path == "" || path == ":memory:" {
		return nil, errors.New("streamlogsqlite: Open needs a file path")
	}
	l := &Log{
		path: path, markerPath: o.MarkerPath, markerGap: o.MarkerEvery, page: o.PageSize,
		now: o.Now, extract: o.Extractor, onConflict: o.OnIDConflict, heads: map[string]uint64{},
	}
	if l.markerPath == "" {
		l.markerPath = path + ".hwm"
	}
	if l.markerGap == 0 {
		l.markerGap = defaultMarkerInterval
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
	var err error
	l.w, err = sqlitekit.OpenWriter(ctx, path, sqlitekit.OpenOptions{CreateParentDir: true})
	if err != nil {
		return nil, fmt.Errorf("streamlogsqlite: open writer: %w", err)
	}
	l.r, err = sqlitekit.OpenReader(ctx, path, sqlitekit.OpenOptions{MaxOpenConns: o.ReadConns})
	if err != nil {
		_ = l.w.Close()
		return nil, fmt.Errorf("streamlogsqlite: open reader: %w", err)
	}
	if err := l.init(ctx, o.OnStorageMismatch); err != nil {
		_ = l.r.Close()
		_ = l.w.Close()
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

func (l *Log) init(ctx context.Context, policy MismatchPolicy) error {
	if _, err := l.w.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("streamlogsqlite: schema: %w", err)
	}
	tx, err := l.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var ver string
	switch err := tx.QueryRowContext(ctx, `SELECT value FROM log_meta WHERE key='schema_version'`).Scan(&ver); {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO log_meta(key,value) VALUES('schema_version',?)`, schemaVersion); err != nil {
			return err
		}
	case err != nil:
		return err
	case ver != schemaVersion:
		return fmt.Errorf("streamlogsqlite: database schema version %q, this build understands %q", ver, schemaVersion)
	}

	rows, err := tx.QueryContext(ctx, `SELECT stream, latest FROM streams`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var s string
		var n uint64
		if err := rows.Scan(&s, &n); err != nil {
			_ = rows.Close()
			return err
		}
		l.heads[s] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()

	fresh := false
	switch err := tx.QueryRowContext(ctx, `SELECT value FROM log_meta WHERE key='epoch'`).Scan(&l.epoch); {
	case errors.Is(err, sql.ErrNoRows):
		fresh = true
	case err != nil:
		return err
	}

	// Compare with the marker kept outside the database file.
	mk, merr := logkit.ReadMarker(l.markerPath)
	if merr != nil && !errors.Is(merr, logkit.ErrMarkerUnreadable) {
		return fmt.Errorf("streamlogsqlite: read marker: %w", merr)
	}
	reason := ""
	switch {
	case errors.Is(merr, logkit.ErrMarkerUnreadable):
		if !fresh {
			reason = "marker_unreadable"
		}
	case fresh && mk != nil && mk.Epoch != "":
		// A marker exists but the database is new: the file was deleted or replaced by an empty one.
		reason = "epoch_differs"
	case !fresh && mk != nil && mk.Epoch != l.epoch:
		reason = "epoch_differs"
	case !fresh && mk != nil:
		if _, behind := mk.Behind(l.heads); behind {
			reason = "head_behind_marker"
		}
	}
	prev := l.epoch
	if mk != nil && fresh {
		prev = mk.Epoch
	}
	if reason != "" && policy == RefuseOnMismatch {
		return fmt.Errorf("%w (%s)", ErrStorageMismatch, reason)
	}
	if fresh || reason != "" {
		if l.epoch, err = mintEpoch(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO log_meta(key,value) VALUES('epoch',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, l.epoch); err != nil {
			return err
		}
		if reason != "" {
			l.recovery = Recovery{Reminted: true, PreviousEpoch: prev, Reason: reason}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	l.dirty = true
	return l.flushMarker(true)
}

// Epoch is the identity of this log; see the package documentation.
func (l *Log) Epoch() string { return l.epoch }

// Recovery reports whether Open re-minted the epoch and why.
func (l *Log) Recovery() Recovery { return l.recovery }

// Stats returns counters since Open.
func (l *Log) Stats() Stats { return Stats{IDConflicts: l.conflicts.Load()} }

// MarkerError returns the last error from writing the marker file, if any.
// A failing marker never fails an Append; it only weakens rollback detection.
func (l *Log) MarkerError() error {
	l.mkMu.Lock()
	defer l.mkMu.Unlock()
	return l.mkError
}

func (l *Log) noteHead(stream string, seq uint64) {
	l.mkMu.Lock()
	l.heads[stream] = seq
	l.dirty = true
	l.mkMu.Unlock()
	_ = l.flushMarker(false)
}

func (l *Log) flushMarker(force bool) error {
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
			return fmt.Errorf("streamlogsqlite: write marker: %w", err)
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

	tx, err := l.w.BeginTx(ctx, nil)
	if err != nil {
		return streamhub.Record{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if e.ID != "" {
		var (
			ostream, okind string
			oseq           uint64
			odigest, odata []byte
			oat            int64
		)
		err := tx.QueryRowContext(ctx, `SELECT stream, seq, kind, digest, at_ns, data FROM records WHERE event_id=?`, e.ID).
			Scan(&ostream, &oseq, &okind, &odigest, &oat, &odata)
		switch {
		case err == nil:
			if ostream == stream && bytes.Equal(odigest, digest) {
				return streamhub.Record{Seq: streamhub.Seq(oseq), Name: okind, Data: odata, At: time.Unix(0, oat).UTC(), ID: e.ID, Epoch: l.epoch}, nil
			}
			l.conflicts.Add(1)
			if l.onConflict != nil {
				l.onConflict(stream, e)
			}
			return streamhub.Record{}, streamhub.ErrEventIDConflict
		case !errors.Is(err, sql.ErrNoRows):
			return streamhub.Record{}, err
		}
	}

	var seq uint64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO streams(stream, latest) VALUES(?, 1)
		 ON CONFLICT(stream) DO UPDATE SET latest = latest + 1 RETURNING latest`, stream).Scan(&seq); err != nil {
		return streamhub.Record{}, err
	}
	at := l.now().UTC()
	var eid any
	if e.ID != "" {
		eid = e.ID
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO records(stream, seq, event_id, kind, at_ns, size, data, digest,
			category, severity, sensitivity, pii, visibility, generation,
			project_id, workstream_id, task_id, session_id, agent_urn, run_id)
		 VALUES(?,?,?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?,?)`,
		stream, seq, eid, e.Name, at.UnixNano(), len(e.Name)+len(data), data, digest,
		cols.Category, cols.Severity, cols.Sensitivity, cols.PII, cols.Visibility, cols.Generation,
		cols.ProjectID, cols.WorkstreamID, cols.TaskID, cols.SessionID, cols.AgentURN, cols.RunID); err != nil {
		return streamhub.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return streamhub.Record{}, err
	}
	l.noteHead(stream, seq)
	return streamhub.Record{Seq: streamhub.Seq(seq), Name: e.Name, Data: data, At: at, ID: e.ID, Epoch: l.epoch}, nil
}

// After implements [streamhub.Log].
func (l *Log) After(ctx context.Context, stream string, after streamhub.Seq) iter.Seq2[streamhub.Record, error] {
	return l.AfterEpoch(ctx, stream, "", after)
}

// AfterCursor is After for an opaque cursor made by [logkit.FormatCursor]. The empty
// cursor reads from the start. A cursor from another epoch yields one
// [streamhub.GapEpochChanged] gap and no records.
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

	tx, err := l.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var latest, pruned uint64
	if err := tx.QueryRowContext(ctx, `SELECT latest, pruned FROM streams WHERE stream=?`, stream).Scan(&latest, &pruned); err != nil && !errors.Is(err, sql.ErrNoRows) {
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
	case uint64(cursor) > latest:
		return nil, gap(streamhub.GapCursorAhead, uint64(cursor)-latest)
	case uint64(cursor) < pruned:
		return nil, gap(streamhub.GapRetention, pruned-uint64(cursor))
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT seq, kind, at_ns, data, event_id FROM records WHERE stream=? AND seq>? ORDER BY seq LIMIT ?`,
		stream, uint64(cursor), l.page)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []streamhub.Record
	for rows.Next() {
		var (
			seq uint64
			r   streamhub.Record
			at  int64
			eid sql.NullString
		)
		if err := rows.Scan(&seq, &r.Name, &at, &r.Data, &eid); err != nil {
			return nil, err
		}
		r.Seq, r.At, r.ID, r.Epoch = streamhub.Seq(seq), time.Unix(0, at).UTC(), eid.String, l.epoch
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
	var latest, pruned, dropped uint64
	err := l.r.QueryRowContext(ctx, `SELECT latest, pruned, dropped FROM streams WHERE stream=?`, stream).Scan(&latest, &pruned, &dropped)
	if errors.Is(err, sql.ErrNoRows) {
		return h, nil
	}
	if err != nil {
		return streamhub.Head{}, err
	}
	h.Latest, h.PrunedThrough, h.Dropped = streamhub.Seq(latest), streamhub.Seq(pruned), dropped
	return h, nil
}

// Trim implements [streamhub.Log]. Records leave from the head only, so the
// cursor space stays contiguous after PrunedThrough.
func (l *Log) Trim(ctx context.Context, stream string, r streamhub.Retention) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := l.enter(); err != nil {
		return err
	}
	defer l.leave()

	tx, err := l.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var latest, pruned uint64
	if err := tx.QueryRowContext(ctx, `SELECT latest, pruned FROM streams WHERE stream=?`, stream).Scan(&latest, &pruned); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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
		thr := l.now().Add(-r.MaxAge).UnixNano()
		if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM records WHERE stream=? AND seq>? AND at_ns<?`, stream, pruned, thr).Scan(&c); err != nil {
			return err
		}
		if c.Valid {
			cut = max(cut, uint64(c.Int64))
		}
	}
	if r.MaxBytes > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT seq, size FROM records WHERE stream=? AND seq>? ORDER BY seq DESC`, stream, pruned)
		if err != nil {
			return err
		}
		total := 0
		for rows.Next() {
			var seq uint64
			var size int
			if err := rows.Scan(&seq, &size); err != nil {
				_ = rows.Close()
				return err
			}
			total += size
			if total > r.MaxBytes {
				cut = max(cut, seq)
				break
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
	}
	if cut <= pruned {
		return nil
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM records WHERE stream=? AND seq<=?`, stream, cut)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx, `UPDATE streams SET pruned=?, dropped=dropped+? WHERE stream=?`, cut, n, stream); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = l.flushMarker(true) // a trim is a natural checkpoint; failure is visible via MarkerError
	return nil
}

// Close implements [streamhub.Log]: it writes the marker and closes both pools.
func (l *Log) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	merr := l.flushMarker(true)
	return errors.Join(merr, l.r.Close(), l.w.Close())
}

// Path is the database file this log was opened on.
func (l *Log) Path() string { return l.path }

// ColumnsOf returns the indexed columns stored for one record.
func (l *Log) ColumnsOf(ctx context.Context, stream string, seq streamhub.Seq) (logkit.Columns, error) {
	if err := l.enter(); err != nil {
		return logkit.Columns{}, err
	}
	defer l.leave()
	var c logkit.Columns
	err := l.r.QueryRowContext(ctx,
		`SELECT category, severity, sensitivity, pii, visibility, generation, project_id, workstream_id, task_id, session_id, agent_urn, run_id
		 FROM records WHERE stream=? AND seq=?`, stream, uint64(seq)).
		Scan(&c.Category, &c.Severity, &c.Sensitivity, &c.PII, &c.Visibility, &c.Generation, &c.ProjectID, &c.WorkstreamID, &c.TaskID, &c.SessionID, &c.AgentURN, &c.RunID)
	return c, err
}

// HasIndex reports whether the named index exists; it exists for tests and
// diagnostics.
func (l *Log) HasIndex(ctx context.Context, name string) bool {
	var n int
	err := l.r.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&n)
	return err == nil && n == 1
}
