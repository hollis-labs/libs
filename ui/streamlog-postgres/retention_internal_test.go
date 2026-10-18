package streamlogpostgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention/storetest"
)

func testDSN(t testing.TB) string {
	t.Helper()
	d := os.Getenv("STREAMLOG_PG_DSN")
	if d == "" {
		if os.Getenv("CI") == "true" {
			t.Fatal("STREAMLOG_PG_DSN is not set in CI: the PostgreSQL service did not start")
		}
		t.Skip("STREAMLOG_PG_DSN not set; skipping PostgreSQL tests")
	}
	return d
}

func openTestLog(t *testing.T, o Options) *Log {
	t.Helper()
	var b [6]byte
	_, _ = rand.Read(b[:])
	dsn := testDSN(t)
	o.Schema = "t_" + hex.EncodeToString(b[:])
	o.MarkerPath = filepath.Join(t.TempDir(), "log.hwm")
	o.MarkerEvery = time.Hour
	o.Warn = func(string) {}
	l, err := OpenDSN(context.Background(), dsn, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = l.Close()
		if p, err := pgxpool.New(context.Background(), dsn); err == nil {
			_, _ = p.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+o.Schema+` CASCADE`)
			p.Close()
		}
	})
	return l
}

type fixture struct {
	t *testing.T
	l *Log
}

func newFixture(t *testing.T) storetest.Fixture { return &fixture{t: t, l: openTestLog(t, Options{})} }

func (f *fixture) Store() retention.Store { return f.l.RetentionStore() }

func (f *fixture) Add(r storetest.Row) (uint64, error) {
	ctx := context.Background()
	tx, err := f.l.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var seq int64
	if err := tx.QueryRow(ctx, `INSERT INTO `+f.l.schema+`.streams(stream, latest) VALUES($1, 1)
		ON CONFLICT (stream) DO UPDATE SET latest = `+f.l.schema+`.streams.latest + 1 RETURNING latest`, r.Stream).Scan(&seq); err != nil {
		return 0, err
	}
	kind := r.Dims.Kind
	if kind == "" {
		kind = "k"
	}
	sum := sha256.Sum256([]byte(fmt.Sprint(r.Stream, seq)))
	_, err = tx.Exec(ctx, `INSERT INTO `+f.l.schema+`.records(stream, seq, kind, at, size, data, digest, class, policy_id, outcome,
		category, severity, sensitivity, pii, project_id, workstream_id, agent_urn)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, $11,$12,$13,$14,$15,$16,$17)`,
		r.Stream, seq, kind, r.At.UTC(), len(r.Payload)+64, r.Payload, sum[:], r.Class, r.PolicyID, r.Dims.Outcome,
		r.Attrs.Category, r.Attrs.Severity, r.Attrs.Sensitivity, r.Attrs.PII, r.Dims.Project, r.Dims.Workstream, r.Dims.Agent)
	if err != nil {
		return 0, err
	}
	return uint64(seq), tx.Commit(ctx)
}

func (f *fixture) Rows(stream string) []storetest.RowState {
	rows, err := f.l.pool.Query(context.Background(), `SELECT seq, class, policy_id, at, category, severity, sensitivity, pii, tier, data, COALESCE(payload_digest,''), rolled
		FROM `+f.l.schema+`.records WHERE stream=$1 ORDER BY seq`, stream)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []storetest.RowState
	for rows.Next() {
		var r storetest.RowState
		var seq int64
		var tier int16
		var at time.Time
		if err := rows.Scan(&seq, &r.Class, &r.PolicyID, &at, &r.Attrs.Category, &r.Attrs.Severity, &r.Attrs.Sensitivity, &r.Attrs.PII, &tier, &r.Payload, &r.PayloadDigest, &r.Rolled); err != nil {
			f.t.Fatal(err)
		}
		r.Seq, r.At, r.Tombstoned = uint64(seq), at.UTC(), tier == 1
		out = append(out, r)
	}
	return out
}

func (f *fixture) Buckets(stream string) []storetest.Bucket {
	rows, err := f.l.pool.Query(context.Background(), `SELECT class, bucket_ns, bucket_start, kind, category, severity, project_id, workstream_id, agent_urn, outcome, count, first_seq, last_seq
		FROM `+f.l.schema+`.rollups WHERE stream=$1 ORDER BY class, bucket_ns, bucket_start`, stream)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []storetest.Bucket
	for rows.Next() {
		var b storetest.Bucket
		var ns int64
		var start time.Time
		if err := rows.Scan(&b.Class, &ns, &start, &b.Dims.Kind, &b.Category, &b.Severity, &b.Dims.Project, &b.Dims.Workstream, &b.Dims.Agent, &b.Dims.Outcome, &b.Count, &b.FirstSeq, &b.LastSeq); err != nil {
			f.t.Fatal(err)
		}
		b.Size, b.Start = retention.Duration(ns), start.UTC()
		out = append(out, b)
	}
	return out
}

func (f *fixture) PrunedThrough(stream string) uint64 {
	var n int64
	if err := f.l.pool.QueryRow(context.Background(), `SELECT pruned FROM `+f.l.schema+`.streams WHERE stream=$1`, stream).Scan(&n); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		f.t.Fatal(err)
	}
	return uint64(n)
}

func TestRetentionStoreConformance(t *testing.T) {
	testDSN(t)
	storetest.Run(t, newFixture)
}

// Two janitors on one database: the advisory lock lets only one run.
func TestJanitorLockIsSharedAcrossInstances(t *testing.T) {
	a := openTestLog(t, Options{})
	dsn := testDSN(t)
	b, err := OpenDSN(context.Background(), dsn, Options{Schema: a.Schema(), MarkerPath: filepath.Join(t.TempDir(), "b.hwm"), Warn: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	unlock, ok, err := a.RetentionStore().TryLock(context.Background())
	if err != nil || !ok {
		t.Fatalf("a TryLock = %v, %v", ok, err)
	}
	if _, ok2, err := b.RetentionStore().TryLock(context.Background()); err != nil || ok2 {
		t.Fatalf("b TryLock = %v, %v; want false without error", ok2, err)
	}
	unlock()
	unlock2, ok3, err := b.RetentionStore().TryLock(context.Background())
	if err != nil || !ok3 {
		t.Fatalf("b TryLock after unlock = %v, %v", ok3, err)
	}
	unlock2()
}

// The janitor on a real log: see the SQLite twin of this test.
func TestJanitorOnALog(t *testing.T) {
	ctx := context.Background()
	policy, err := retention.Config{}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := openTestLog(t, Options{Now: func() time.Time { return clock }, Retention: policy, RetentionPolicyID: "v1"})

	ev := func(id, cat, sev string) streamhub.Event {
		return streamhub.Event{ID: id, Name: "k", Data: []byte(fmt.Sprintf(
			`{"class":{"category":%q,"severity":%q},"scope":{"project_id":"p1"},"payload":{"to":"doing"}}`, cat, sev))}
	}
	l.Append(ctx, "env", ev("e1", "tool", "debug"))
	clock = clock.Add(time.Hour)
	dbg2, _ := l.Append(ctx, "env", ev("e2", "tool", "debug"))
	clock = clock.Add(time.Hour)
	audit, _ := l.Append(ctx, "env", ev("e3", "audit", "info"))
	clock = clock.Add(time.Hour)
	dbg3, _ := l.Append(ctx, "env", ev("e4", "tool", "debug"))

	clock = clock.Add(20 * 24 * time.Hour)
	rep, err := retention.NewJanitor(l.RetentionStore(), retention.NewPlanner(policy)).Run(ctx, clock)
	if err != nil {
		t.Fatal(err)
	}
	dbg := rep.Classes[retention.ClassDebug]
	if dbg == nil || dbg.Trimmed != 2 || dbg.RolledUp != 3 || rep.HeldByHead != 1 || dbg.Errors != 0 {
		t.Fatalf("debug report = %+v, held %d", dbg, rep.HeldByHead)
	}
	head, _ := l.Head(ctx, "env")
	if head.PrunedThrough != dbg2.Seq || head.Latest != dbg3.Seq {
		t.Fatalf("head = %+v", head)
	}
	var seen []streamhub.Record
	for r, err := range l.After(ctx, "env", head.PrunedThrough) {
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, r)
	}
	if len(seen) != 2 || seen[0].Seq != audit.Seq || len(seen[0].Data) == 0 || seen[1].Data != nil {
		t.Fatalf("records after the trim = %+v", seen)
	}
	for _, err := range l.After(ctx, "env", 0) {
		var ge *streamhub.GapError
		if !errors.As(err, &ge) || ge.Gap.Reason != streamhub.GapRetention || ge.Gap.OldestAvailable != head.PrunedThrough+1 {
			t.Fatalf("gap = %v", err)
		}
		break
	}
	if again, err := l.Append(ctx, "env", ev("e4", "tool", "debug")); err != nil || again.Seq != dbg3.Seq {
		t.Fatalf("retry of a tombstoned id = %+v, %v", again, err)
	}
	var outcome string
	if err := l.pool.QueryRow(ctx, `SELECT outcome FROM `+l.schema+`.rollups WHERE stream='env' AND class=$1 LIMIT 1`, retention.ClassDebug).Scan(&outcome); err != nil || outcome != "doing" {
		t.Fatalf("rollup outcome = %q, %v", outcome, err)
	}
}
