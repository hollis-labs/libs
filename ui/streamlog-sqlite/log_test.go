package streamlogsqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	streamlog "github.com/hollis-labs/libs/ui/streamlog-sqlite"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/hubtest"
)

var ctx = context.Background()

func open(t *testing.T, path string, o streamlog.Options) *streamlog.Log {
	t.Helper()
	if o.MarkerEvery == 0 {
		o.MarkerEvery = -1 // marker after every append: deterministic tests
	}
	l, err := streamlog.Open(ctx, path, o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return l
}

// slow marks backends used by the generic suites: they append thousands of
// records, so the marker is written on a short interval, not after every append.
func slow(o streamlog.Options) streamlog.Options {
	o.MarkerEvery = 50 * time.Millisecond
	return o
}

func factory(pageSize int) hubtest.Factory {
	return hubtest.Factory{
		Idempotent: true,
		New: func(t *testing.T) streamhub.Log {
			return open(t, filepath.Join(t.TempDir(), "log.db"), slow(streamlog.Options{PageSize: pageSize}))
		},
		Reopen: func(t *testing.T, l streamhub.Log) streamhub.Log {
			sl := l.(*streamlog.Log)
			path := sl.Path()
			if err := sl.Close(); err != nil {
				t.Fatal(err)
			}
			return open(t, path, slow(streamlog.Options{PageSize: pageSize}))
		},
	}
}

func TestConformance(t *testing.T) {
	t.Run("page7", func(t *testing.T) { hubtest.Conformance(t, factory(7)) })
	t.Run("defaultPage", func(t *testing.T) { hubtest.Conformance(t, factory(0)) })
}

func TestHubSuite(t *testing.T) { hubtest.HubSuite(t, factory(16)) }

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

// A restart keeps cursors and epoch: a consumer resumes exactly where it was.
func TestRestartKeepsCursorsAndEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	l := open(t, path, streamlog.Options{})
	for range 5 {
		if _, err := l.Append(ctx, "env", streamhub.Event{Name: "k", Data: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	epoch := l.Epoch()
	cursor := streamlog.FormatCursor(epoch, 3)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	l = open(t, path, streamlog.Options{})
	defer l.Close()
	if l.Epoch() != epoch || l.Recovery().Reminted {
		t.Fatalf("epoch %q (reminted %+v), want %q unchanged", l.Epoch(), l.Recovery(), epoch)
	}
	got, err := collect(l, "env", cursor)
	if err != nil || len(got) != 2 || got[0].Seq != 4 || got[1].Seq != 5 {
		t.Fatalf("resume = %+v, %v; want 4,5", got, err)
	}
	r, err := l.Append(ctx, "env", streamhub.Event{Name: "k"})
	if err != nil || r.Seq != 6 {
		t.Fatalf("Append after restart = %+v, %v; want Seq 6", r, err)
	}
}

// Trim is head-only; the gap is computed and always carries reason,
// earliest_available, high_water and epoch.
func TestTrimGapIsNeverEmpty(t *testing.T) {
	l := open(t, filepath.Join(t.TempDir(), "log.db"), streamlog.Options{})
	defer l.Close()
	for range 10 {
		l.Append(ctx, "env", streamhub.Event{Name: "k"})
	}
	if err := l.Trim(ctx, "env", streamhub.Retention{MaxRecords: 3}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"", streamlog.FormatCursor(l.Epoch(), 2)} {
		_, err := collect(l, "env", c)
		g := gapOf(t, err)
		if g.Reason != streamhub.GapRetention || g.OldestAvailable != 8 || g.Latest != 10 || g.Epoch != l.Epoch() || g.Missed == 0 {
			t.Fatalf("cursor %q gap = %+v", c, g)
		}
	}
	_, err := collect(l, "env", streamlog.FormatCursor(l.Epoch(), 99))
	if g := gapOf(t, err); g.Reason != streamhub.GapCursorAhead || g.Latest != 10 || g.OldestAvailable != 8 {
		t.Fatalf("ahead gap = %+v", g)
	}
}

// Deleting the database file (recreate) gives a new epoch; the old cursor
// gets epoch_changed, not records from a different history.
func TestRecreateRemintsEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	l := open(t, path, streamlog.Options{})
	l.Append(ctx, "env", streamhub.Event{Name: "k"})
	l.Append(ctx, "env", streamhub.Event{Name: "k"})
	old := streamlog.FormatCursor(l.Epoch(), 2)
	oldEpoch := l.Epoch()
	l.Close()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Remove(p)
	}

	l = open(t, path, streamlog.Options{})
	defer l.Close()
	if l.Epoch() == oldEpoch {
		t.Fatal("epoch not re-minted after the database was recreated")
	}
	if rec := l.Recovery(); !rec.Reminted || rec.PreviousEpoch != oldEpoch || rec.Reason != "epoch_differs" {
		t.Fatalf("recovery = %+v", rec)
	}
	l.Append(ctx, "env", streamhub.Event{Name: "other history"})
	_, err := collect(l, "env", old)
	g := gapOf(t, err)
	if g.Reason != streamhub.GapEpochChanged || g.OldestAvailable != 1 || g.Latest != 1 || g.Epoch != l.Epoch() {
		t.Fatalf("stale-epoch gap = %+v", g)
	}
}

// Restoring an OLDER copy of the database under the same epoch would reuse
// cursor values for different events. The marker outside the file catches it.
func TestRestoreOfOlderCopyIsDetected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy streamlog.MismatchPolicy
	}{{"remint", streamlog.RemintOnMismatch}, {"refuse", streamlog.RefuseOnMismatch}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "log.db")
			l := open(t, path, streamlog.Options{})
			l.Append(ctx, "env", streamhub.Event{Name: "a"})
			l.Close()
			backup := filepath.Join(dir, "backup.db")
			copyFile(t, path, backup)

			l = open(t, path, streamlog.Options{})
			epoch := l.Epoch()
			l.Append(ctx, "env", streamhub.Event{Name: "b"})
			l.Append(ctx, "env", streamhub.Event{Name: "c"})
			l.Close()

			// "Restore": put the older copy back, leave the marker as it was.
			for _, p := range []string{path + "-wal", path + "-shm"} {
				_ = os.Remove(p)
			}
			copyFile(t, backup, path)

			l, err := streamlog.Open(ctx, path, streamlog.Options{OnStorageMismatch: tc.policy, MarkerEvery: -1})
			if tc.policy == streamlog.RefuseOnMismatch {
				if !errors.Is(err, streamlog.ErrStorageMismatch) {
					t.Fatalf("Open = %v, want ErrStorageMismatch", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			if l.Epoch() == epoch || l.Recovery().Reason != "head_behind_marker" {
				t.Fatalf("epoch %q recovery %+v; want a new epoch because the head is behind the marker", l.Epoch(), l.Recovery())
			}
			// A consumer that had read cursor 3 under the old epoch is not served cursor 2's new meaning.
			l.Append(ctx, "env", streamhub.Event{Name: "different event"})
			_, gerr := collect(l, "env", streamlog.FormatCursor(epoch, 2))
			if g := gapOf(t, gerr); g.Reason != streamhub.GapEpochChanged {
				t.Fatalf("gap = %+v", g)
			}
		})
	}
}

func TestFirstOpenAdoptsMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	l := open(t, path, streamlog.Options{})
	if l.Recovery().Reminted {
		t.Fatalf("fresh database reported recovery %+v", l.Recovery())
	}
	l.Close()
	if _, err := os.Stat(path + ".hwm"); err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	// Removing only the marker is not a mismatch: it is re-adopted.
	_ = os.Remove(path + ".hwm")
	l = open(t, path, streamlog.Options{OnStorageMismatch: streamlog.RefuseOnMismatch})
	defer l.Close()
	if l.Recovery().Reminted {
		t.Fatalf("missing marker caused recovery %+v", l.Recovery())
	}
}

func TestEventIDConflictIsCountedAndReported(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	l := open(t, filepath.Join(t.TempDir(), "log.db"), streamlog.Options{
		OnIDConflict: func(stream string, e streamhub.Event) { mu.Lock(); seen = append(seen, stream+"/"+e.ID); mu.Unlock() },
	})
	defer l.Close()
	first, _ := l.Append(ctx, "env", streamhub.Event{ID: "evt_1", Name: "k", Data: []byte("a")})
	again, err := l.Append(ctx, "env", streamhub.Event{ID: "evt_1", Name: "k", Data: []byte("a")})
	if err != nil || again.Seq != first.Seq || !again.At.Equal(first.At) {
		t.Fatalf("same content = %+v, %v; want the original record", again, err)
	}
	if l.Stats().IDConflicts != 0 {
		t.Fatal("a plain retry was counted as a conflict")
	}
	for range 2 {
		if _, err := l.Append(ctx, "env", streamhub.Event{ID: "evt_1", Name: "k", Data: []byte("b")}); !errors.Is(err, streamhub.ErrEventIDConflict) {
			t.Fatalf("different content err = %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if l.Stats().IDConflicts != 2 || len(seen) != 2 || seen[0] != "env/evt_1" {
		t.Fatalf("conflicts = %d, hook = %v", l.Stats().IDConflicts, seen)
	}
}

// Idempotency survives a restart: a producer replaying after a crash gets the
// original cursor back, and no duplicate row.
func TestEventIDSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	l := open(t, path, streamlog.Options{})
	first, _ := l.Append(ctx, "env", streamhub.Event{ID: "evt_1", Name: "k", Data: []byte("a")})
	l.Close()
	l = open(t, path, streamlog.Options{})
	defer l.Close()
	again, err := l.Append(ctx, "env", streamhub.Event{ID: "evt_1", Name: "k", Data: []byte("a")})
	if err != nil || again.Seq != first.Seq {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if h, _ := l.Head(ctx, "env"); h.Latest != 1 {
		t.Fatalf("head = %+v", h)
	}
}

func TestEnvelopeColumnsAreIndexed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	l := open(t, path, streamlog.Options{})
	defer l.Close()
	_, err := l.Append(ctx, "env", streamhub.Event{Name: "task.state_changed", Data: []byte(
		`{"session_id":"s1","generation":3,"scope":{"project_id":"p1","task_id":"t1","agent_urn":"a"},"class":{"category":"state","severity":"info","visibility":"environment"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := l.ColumnsOf(ctx, "env", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := streamlog.Columns{Category: "state", Severity: "info", Visibility: "environment", Generation: 3,
		ProjectID: "p1", TaskID: "t1", SessionID: "s1", AgentURN: "a"}
	if cols != want {
		t.Fatalf("columns = %+v, want %+v", cols, want)
	}
}

func TestParseCursor(t *testing.T) {
	e, s, err := streamlog.ParseCursor(streamlog.FormatCursor("abc", 7))
	if err != nil || e != "abc" || s != 7 {
		t.Fatalf("round trip = %q %d %v", e, s, err)
	}
	if e, s, err := streamlog.ParseCursor(""); err != nil || e != "" || s != 0 {
		t.Fatalf("empty = %q %d %v", e, s, err)
	}
	for _, bad := range []string{"nocolon", ":5", "abc:x", "abc:-1"} {
		if _, _, err := streamlog.ParseCursor(bad); !errors.Is(err, streamlog.ErrBadCursor) {
			t.Fatalf("ParseCursor(%q) err = %v", bad, err)
		}
	}
}

func TestMaxAgeTrim(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := open(t, filepath.Join(t.TempDir(), "log.db"), streamlog.Options{Now: func() time.Time { return now }})
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

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
}

// A retry that re-marshals the event (key order, whitespace) is a duplicate,
// not a conflict; a real change is still a conflict; non-JSON is byte-exact.
func TestEventIDDigestIsCanonicalForJSON(t *testing.T) {
	l := open(t, filepath.Join(t.TempDir(), "log.db"), streamlog.Options{})
	defer l.Close()
	first, err := l.Append(ctx, "env", streamhub.Event{ID: "e1", Name: "k", Data: []byte(`{"a":1,"b":{"x":[1,2],"y":"z"},"n":1.50}`)})
	if err != nil {
		t.Fatal(err)
	}
	again, err := l.Append(ctx, "env", streamhub.Event{ID: "e1", Name: "k", Data: []byte("{ \"n\": 1.50,\n \"b\": {\"y\":\"z\", \"x\": [1, 2]}, \"a\": 1 }")})
	if err != nil || again.Seq != first.Seq {
		t.Fatalf("reordered keys/whitespace = %+v, %v; want the original record", again, err)
	}
	if l.Stats().IDConflicts != 0 {
		t.Fatal("re-marshaled retry was counted as a conflict")
	}
	for name, data := range map[string]string{
		"value":  `{"a":2,"b":{"x":[1,2],"y":"z"},"n":1.50}`,
		"array":  `{"a":1,"b":{"x":[2,1],"y":"z"},"n":1.50}`,
		"number": `{"a":1,"b":{"x":[1,2],"y":"z"},"n":1.5}`, // literal text differs: not silently equal
	} {
		if _, err := l.Append(ctx, "env", streamhub.Event{ID: "e1", Name: "k", Data: []byte(data)}); !errors.Is(err, streamhub.ErrEventIDConflict) {
			t.Fatalf("%s change err = %v, want ErrEventIDConflict", name, err)
		}
	}
	if _, err := l.Append(ctx, "env", streamhub.Event{ID: "t1", Name: "k", Data: []byte("a  b")}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(ctx, "env", streamhub.Event{ID: "t1", Name: "k", Data: []byte("a b")}); !errors.Is(err, streamhub.ErrEventIDConflict) {
		t.Fatalf("non-JSON whitespace change err = %v, want byte-exact conflict", err)
	}
}

// mesh.Event keeps visibility at the top level; envelopes with class.visibility win.
func TestExtractorReadsMeshTopLevelVisibility(t *testing.T) {
	mesh := streamhub.Event{Name: "task.state_changed", Data: []byte(
		`{"schema_version":"1","id":"evt_1","kind":"task.state_changed","session_id":"s1","generation":2,"visibility":"environment",` +
			`"scope":{"project_id":"p1","workstream_id":"w1","agent_urn":"msg://agent/x/y"},"class":{"category":"state","severity":"info","sensitivity":"private"}}`)}
	c := streamlog.EnvelopeExtractor(mesh)
	if c.Visibility != "environment" || c.WorkstreamID != "w1" || c.AgentURN != "msg://agent/x/y" || c.Category != "state" || c.Generation != 2 {
		t.Fatalf("mesh columns = %+v", c)
	}
	both := streamhub.Event{Data: []byte(`{"visibility":"mesh","class":{"visibility":"operator"}}`)}
	if v := streamlog.EnvelopeExtractor(both).Visibility; v != "operator" {
		t.Fatalf("class.visibility should win, got %q", v)
	}
}

func TestWorkstreamAndAgentIndexesExist(t *testing.T) {
	l := open(t, filepath.Join(t.TempDir(), "log.db"), streamlog.Options{})
	defer l.Close()
	for _, idx := range []string{"records_workstream", "records_agent", "records_project", "records_task", "records_session", "records_kind"} {
		if !l.HasIndex(ctx, idx) {
			t.Errorf("index %s missing", idx)
		}
	}
}

// With a long marker interval, appends do not touch the marker; Close does.
func TestCloseAndTrimFlushTheMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	l := open(t, path, streamlog.Options{MarkerEvery: time.Hour})
	for range 3 {
		l.Append(ctx, "env", streamhub.Event{Name: "k"})
	}
	if got := markerHead(t, path); got != 0 {
		t.Fatalf("marker head %d before any flush, want the throttled value 0", got)
	}
	if err := l.Trim(ctx, "env", streamhub.Retention{MaxRecords: 2}); err != nil {
		t.Fatal(err)
	}
	if got := markerHead(t, path); got != 3 {
		t.Fatalf("marker head %d after Trim, want 3", got)
	}
	l.Append(ctx, "env", streamhub.Event{Name: "k"})
	if got := markerHead(t, path); got != 3 {
		t.Fatalf("marker head %d before Close, want 3 (throttled)", got)
	}
	l.Close()
	if got := markerHead(t, path); got != 4 {
		t.Fatalf("marker head %d after Close, want 4", got)
	}
}

func markerHead(t *testing.T, dbPath string) uint64 {
	t.Helper()
	b, err := os.ReadFile(dbPath + ".hwm")
	if err != nil {
		t.Fatal(err)
	}
	var m struct{ Streams map[string]uint64 }
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m.Streams["env"]
}
