package streamlogpostgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	streamlog "github.com/hollis-labs/libs/ui/streamlog-postgres"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/hubtest"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/logkit"
)

var ctx = context.Background()

// dsn returns the test database, or skips: these tests need a PostgreSQL
// server (STREAMLOG_PG_DSN=postgres://user:pass@host:port/db).
func dsn(t testing.TB) string {
	t.Helper()
	d := os.Getenv("STREAMLOG_PG_DSN")
	if d == "" {
		t.Skip("STREAMLOG_PG_DSN not set; skipping PostgreSQL tests")
	}
	return d
}

func schemaName() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "t_" + hex.EncodeToString(b[:])
}

// env is one isolated schema plus its marker file.
type env struct {
	t      *testing.T
	dsn    string
	schema string
	marker string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, dsn: dsn(t), schema: schemaName(), marker: filepath.Join(t.TempDir(), "log.hwm")}
	t.Cleanup(func() {
		p, err := pgxpool.New(ctx, e.dsn)
		if err == nil {
			_, _ = p.Exec(ctx, `DROP SCHEMA IF EXISTS `+e.schema+` CASCADE`)
			p.Close()
		}
	})
	return e
}

func (e *env) open(o streamlog.Options) *streamlog.Log {
	e.t.Helper()
	o.Schema, o.MarkerPath = e.schema, e.marker
	if o.MarkerEvery == 0 {
		o.MarkerEvery = -1
	}
	if o.Warn == nil {
		o.Warn = func(string) {}
	}
	l, err := streamlog.OpenDSN(ctx, e.dsn, o)
	if err != nil {
		e.t.Fatalf("OpenDSN: %v", err)
	}
	return l
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	p, err := pgxpool.New(ctx, e.dsn)
	if err != nil {
		e.t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Exec(ctx, sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func factory(pageSize int) hubtest.Factory {
	return hubtest.Factory{
		Idempotent: true,
		New: func(t *testing.T) streamhub.Log {
			e := newEnv(t)
			return e.open(streamlog.Options{PageSize: pageSize, MarkerEvery: 50 * time.Millisecond})
		},
		Reopen: func(t *testing.T, l streamhub.Log) streamhub.Log {
			// Same schema and marker as l: it was opened by newEnv's helper, so look them up.
			pl := l.(*streamlog.Log)
			if err := pl.Close(); err != nil {
				t.Fatal(err)
			}
			e := &env{t: t, dsn: dsn(t), schema: pl.Schema(), marker: pl.MarkerPath()}
			return e.open(streamlog.Options{PageSize: pageSize, MarkerEvery: 50 * time.Millisecond})
		},
	}
}

func TestConformance(t *testing.T) {
	dsn(t)
	t.Run("page7", func(t *testing.T) { hubtest.Conformance(t, factory(7)) })
	t.Run("defaultPage", func(t *testing.T) { hubtest.Conformance(t, factory(0)) })
}

func TestHubSuite(t *testing.T) {
	dsn(t)
	hubtest.HubSuite(t, factory(16))
}

func collect(l *streamlog.Log, stream, cursor string) ([]streamhub.Record, error) {
	var out []streamhub.Record
	for r, err := range l.AfterCursor(ctx, stream, cursor) {
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

func gapOf(t *testing.T, err error) streamhub.Gap {
	t.Helper()
	var ge *streamhub.GapError
	if !errors.As(err, &ge) {
		t.Fatalf("err = %v, want a *GapError", err)
	}
	return ge.Gap
}

func appendN(t *testing.T, l *streamlog.Log, n int) {
	t.Helper()
	for range n {
		if _, err := l.Append(ctx, "env", streamhub.Event{Name: "k", Data: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRestartKeepsCursorsAndEpoch(t *testing.T) {
	e := newEnv(t)
	l := e.open(streamlog.Options{})
	appendN(t, l, 5)
	epoch := l.Epoch()
	cursor := logkit.FormatCursor(epoch, 3)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l = e.open(streamlog.Options{})
	defer l.Close()
	if l.Epoch() != epoch || l.Recovery().Reminted {
		t.Fatalf("epoch %q recovery %+v; want %q unchanged", l.Epoch(), l.Recovery(), epoch)
	}
	got, err := collect(l, "env", cursor)
	if err != nil || len(got) != 2 || got[0].Seq != 4 || got[1].Seq != 5 {
		t.Fatalf("resume = %+v, %v", got, err)
	}
	if r, err := l.Append(ctx, "env", streamhub.Event{Name: "k"}); err != nil || r.Seq != 6 {
		t.Fatalf("Append after restart = %+v, %v", r, err)
	}
}

func TestTrimGapIsNeverEmpty(t *testing.T) {
	l := newEnv(t).open(streamlog.Options{})
	defer l.Close()
	appendN(t, l, 10)
	if err := l.Trim(ctx, "env", streamhub.Retention{MaxRecords: 3}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"", logkit.FormatCursor(l.Epoch(), 2)} {
		_, err := collect(l, "env", c)
		g := gapOf(t, err)
		if g.Reason != streamhub.GapRetention || g.OldestAvailable != 8 || g.Latest != 10 || g.Epoch != l.Epoch() || g.Missed == 0 {
			t.Fatalf("cursor %q gap = %+v", c, g)
		}
	}
}

// --- restore detection: each path ---

func reopenMinted(t *testing.T, e *env, o streamlog.Options, wantReason string) (*streamlog.Log, string) {
	t.Helper()
	l := e.open(o)
	rec := l.Recovery()
	if !rec.Reminted || rec.Reason != wantReason {
		t.Fatalf("recovery = %+v, want reminted for %q", rec, wantReason)
	}
	return l, rec.PreviousEpoch
}

func prepared(t *testing.T) (*env, string) {
	e := newEnv(t)
	l := e.open(streamlog.Options{})
	appendN(t, l, 3)
	epoch := l.Epoch()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return e, epoch
}

func staleCursorGap(t *testing.T, l *streamlog.Log, oldEpoch string) {
	t.Helper()
	appendN(t, l, 1)
	_, err := collect(l, "env", logkit.FormatCursor(oldEpoch, 2))
	if g := gapOf(t, err); g.Reason != streamhub.GapEpochChanged || g.Epoch != l.Epoch() || g.Latest != 1 {
		t.Fatalf("stale-epoch gap = %+v", g)
	}
}

// A different cluster (system identifier) is detected from the database.
func TestClusterSwapRemints(t *testing.T) {
	e, old := prepared(t)
	e.exec(`UPDATE ` + e.schema + `.log_meta SET value='1' WHERE key='pg_system_identifier'`)
	l, prev := reopenMinted(t, e, streamlog.Options{}, "system_identifier_changed")
	defer l.Close()
	if prev != old || l.Epoch() == old {
		t.Fatalf("previous %q epoch %q, old %q", prev, l.Epoch(), old)
	}
	e.exec(`DELETE FROM ` + e.schema + `.records`) // keep the next assertions about the new epoch only
	e.exec(`UPDATE ` + e.schema + `.streams SET latest=0, pruned=0`)
}

// A point-in-time recovery or promotion starts a new timeline.
func TestTimelineChangeRemints(t *testing.T) {
	e, old := prepared(t)
	e.exec(`UPDATE ` + e.schema + `.log_meta SET value='99' WHERE key='pg_timeline'`)
	l, _ := reopenMinted(t, e, streamlog.Options{}, "timeline_changed")
	defer l.Close()
	if l.Epoch() == old {
		t.Fatal("epoch not re-minted")
	}
	// The new identity is recorded: a further reopen is clean.
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	again := e.open(streamlog.Options{})
	defer again.Close()
	if again.Recovery().Reminted {
		t.Fatalf("second open recovery = %+v", again.Recovery())
	}
}

// A pg_restore into the same cluster or a snapshot rollback changes neither
// identifier: only the external marker sees it.
func TestSnapshotRollbackCaughtByMarker(t *testing.T) {
	e, old := prepared(t)
	e.exec(`DELETE FROM ` + e.schema + `.records WHERE seq > 1`)
	e.exec(`UPDATE ` + e.schema + `.streams SET latest=1`)
	l, _ := reopenMinted(t, e, streamlog.Options{}, "head_behind_marker")
	defer l.Close()
	if l.Epoch() == old {
		t.Fatal("epoch not re-minted")
	}
}

func TestRecreatedSchemaRemints(t *testing.T) {
	e, old := prepared(t)
	e.exec(`DROP SCHEMA ` + e.schema + ` CASCADE`)
	l, _ := reopenMinted(t, e, streamlog.Options{}, "epoch_differs")
	defer l.Close()
	staleCursorGap(t, l, old)
}

func TestRefuseOption(t *testing.T) {
	e, _ := prepared(t)
	e.exec(`UPDATE ` + e.schema + `.log_meta SET value='99' WHERE key='pg_timeline'`)
	if _, err := streamlog.OpenDSN(ctx, e.dsn, streamlog.Options{Schema: e.schema, MarkerPath: e.marker, OnStorageMismatch: streamlog.RefuseOnMismatch, Warn: func(string) {}}); !errors.Is(err, streamlog.ErrStorageMismatch) {
		t.Fatalf("OpenDSN = %v, want ErrStorageMismatch", err)
	}
}

// With the marker disabled the rollback goes unseen, and Open says so.
func TestMarkerDisabledWarnsAndMissesRollback(t *testing.T) {
	e, old := prepared(t)
	e.exec(`DELETE FROM ` + e.schema + `.records WHERE seq > 1`)
	e.exec(`UPDATE ` + e.schema + `.streams SET latest=1`)
	var warned []string
	l := e.open(streamlog.Options{DisableMarker: true, Warn: func(m string) { warned = append(warned, m) }})
	defer l.Close()
	if l.Recovery().Reminted || l.Epoch() != old {
		t.Fatalf("a disabled marker cannot see the rollback; recovery %+v", l.Recovery())
	}
	if len(warned) == 0 || len(l.Recovery().Warnings) == 0 {
		t.Fatalf("no startup warning (%v)", warned)
	}
}

func TestUnreadableMarkerRemints(t *testing.T) {
	e, _ := prepared(t)
	if err := os.WriteFile(e.marker, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, _ := reopenMinted(t, e, streamlog.Options{}, "marker_unreadable")
	l.Close()
}

func TestMissingMarkerIsReadopted(t *testing.T) {
	e, old := prepared(t)
	_ = os.Remove(e.marker)
	l := e.open(streamlog.Options{OnStorageMismatch: streamlog.RefuseOnMismatch})
	defer l.Close()
	if l.Recovery().Reminted || l.Epoch() != old {
		t.Fatalf("missing marker caused recovery %+v", l.Recovery())
	}
}

// --- idempotency ---

func TestEventIDConflictIsCountedAndReported(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	l := newEnv(t).open(streamlog.Options{OnIDConflict: func(s string, ev streamhub.Event) { mu.Lock(); seen = append(seen, s+"/"+ev.ID); mu.Unlock() }})
	defer l.Close()
	first, _ := l.Append(ctx, "env", streamhub.Event{ID: "evt_1", Name: "k", Data: []byte(`{"a":1,"b":2}`)})
	again, err := l.Append(ctx, "env", streamhub.Event{ID: "evt_1", Name: "k", Data: []byte(`{ "b": 2, "a": 1 }`)})
	if err != nil || again.Seq != first.Seq || !again.At.Equal(first.At) {
		t.Fatalf("re-marshaled retry = %+v, %v; want the original record", again, err)
	}
	if l.Stats().IDConflicts != 0 {
		t.Fatal("a retry was counted as a conflict")
	}
	for range 2 {
		if _, err := l.Append(ctx, "env", streamhub.Event{ID: "evt_1", Name: "k", Data: []byte(`{"a":2,"b":2}`)}); !errors.Is(err, streamhub.ErrEventIDConflict) {
			t.Fatalf("err = %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if l.Stats().IDConflicts != 2 || len(seen) != 2 {
		t.Fatalf("conflicts %d hook %v", l.Stats().IDConflicts, seen)
	}
}

// Concurrent producers retrying the same id produce exactly one row.
func TestConcurrentSameIDAppendsOnce(t *testing.T) {
	l := newEnv(t).open(streamlog.Options{})
	defer l.Close()
	var wg sync.WaitGroup
	var seqs [16]atomic.Uint64
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := l.Append(ctx, "env", streamhub.Event{ID: "evt_same", Name: "k", Data: []byte(`{"x":1}`)})
			if err != nil {
				t.Errorf("Append: %v", err)
				return
			}
			seqs[i].Store(uint64(r.Seq))
		}()
	}
	wg.Wait()
	for i := range seqs {
		if seqs[i].Load() != 1 {
			t.Fatalf("goroutine %d got seq %d, want 1", i, seqs[i].Load())
		}
	}
	if h, _ := l.Head(ctx, "env"); h.Latest != 1 {
		t.Fatalf("head = %+v, want exactly one record", h)
	}
}

// Several log instances (processes) writing one stream still get gap-free, ordered cursors.
func TestTwoWritersShareOneGapFreeSequence(t *testing.T) {
	e := newEnv(t)
	a := e.open(streamlog.Options{})
	defer a.Close()
	e2 := &env{t: t, dsn: e.dsn, schema: e.schema, marker: filepath.Join(t.TempDir(), "other.hwm")}
	b := e2.open(streamlog.Options{})
	defer b.Close()
	var wg sync.WaitGroup
	for _, l := range []*streamlog.Log{a, b} {
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 25 {
					if _, err := l.Append(ctx, "env", streamhub.Event{Name: "k"}); err != nil {
						t.Errorf("Append: %v", err)
						return
					}
				}
			}()
		}
	}
	wg.Wait()
	got, err := collect(a, "env", "")
	if err != nil || len(got) != 200 {
		t.Fatalf("read %d records, %v; want 200", len(got), err)
	}
	for i, r := range got {
		if r.Seq != streamhub.Seq(i+1) {
			t.Fatalf("record %d has seq %d", i, r.Seq)
		}
	}
}

func TestEnvelopeColumnsAndIndexes(t *testing.T) {
	l := newEnv(t).open(streamlog.Options{})
	defer l.Close()
	if _, err := l.Append(ctx, "env", streamhub.Event{Name: "task.state_changed", Data: []byte(
		`{"session_id":"s1","generation":3,"visibility":"environment","scope":{"project_id":"p1","workstream_id":"w1","task_id":"t1","agent_urn":"a"},"class":{"category":"state","severity":"info"}}`)}); err != nil {
		t.Fatal(err)
	}
	c, err := l.ColumnsOf(ctx, "env", 1)
	want := logkit.Columns{Category: "state", Severity: "info", Visibility: "environment", Generation: 3, ProjectID: "p1", WorkstreamID: "w1", TaskID: "t1", SessionID: "s1", AgentURN: "a"}
	if err != nil || c != want {
		t.Fatalf("columns = %+v, %v", c, err)
	}
	for _, idx := range []string{"records_event_id", "records_project", "records_workstream", "records_task", "records_session", "records_agent", "records_kind"} {
		if !l.HasIndex(ctx, idx) {
			t.Errorf("index %s missing", idx)
		}
	}
}

// Non-JSON data is stored byte-exact; NUL escapes never reach JSONB.
func TestOpaqueDataIsStoredExactly(t *testing.T) {
	l := newEnv(t).open(streamlog.Options{})
	defer l.Close()
	for i, d := range [][]byte{[]byte("plain text \x00 with nul"), []byte(`{"s":"a\u0000b"}`), []byte(`{"ok":true}`), {0xff, 0xfe}} {
		r, err := l.Append(ctx, "env", streamhub.Event{Name: fmt.Sprint("k", i), Data: d})
		if err != nil {
			t.Fatalf("data %q: %v", d, err)
		}
		got, err := collect(l, "env", logkit.FormatCursor(l.Epoch(), r.Seq-1))
		if err != nil || len(got) != 1 || string(got[0].Data) != string(d) {
			t.Fatalf("data %q read back as %+v, %v", d, got, err)
		}
	}
}

func TestMaxAgeTrim(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newEnv(t).open(streamlog.Options{Now: func() time.Time { return now }})
	defer l.Close()
	l.Append(ctx, "env", streamhub.Event{})
	now = now.Add(time.Hour)
	l.Append(ctx, "env", streamhub.Event{})
	now = now.Add(time.Minute)
	if err := l.Trim(ctx, "env", streamhub.Retention{MaxAge: 30 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if h, _ := l.Head(ctx, "env"); h.PrunedThrough != 1 || h.Latest != 2 || h.Dropped != 1 {
		t.Fatalf("head = %+v", h)
	}
}
