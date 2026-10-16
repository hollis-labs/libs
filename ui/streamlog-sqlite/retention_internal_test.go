package streamlogsqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention/storetest"
)

// fixture seeds and inspects a Log's tables directly, so the conformance
// suite can place rows at any time and class.
type fixture struct {
	t *testing.T
	l *Log
}

func newFixture(t *testing.T) storetest.Fixture {
	l, err := Open(context.Background(), filepath.Join(t.TempDir(), "log.db"), Options{MarkerEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return &fixture{t: t, l: l}
}

func (f *fixture) Store() retention.Store { return f.l.RetentionStore() }

func (f *fixture) Add(r storetest.Row) (uint64, error) {
	ctx := context.Background()
	tx, err := f.l.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var seq int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO streams(stream, latest) VALUES(?, 1)
		ON CONFLICT(stream) DO UPDATE SET latest = latest + 1 RETURNING latest`, r.Stream).Scan(&seq); err != nil {
		return 0, err
	}
	kind := r.Dims.Kind
	if kind == "" {
		kind = "k"
	}
	sum := sha256.Sum256([]byte(fmt.Sprint(r.Stream, seq)))
	_, err = tx.ExecContext(ctx, `INSERT INTO records(stream, seq, kind, at_ns, size, data, digest, class, policy_id, outcome,
		category, severity, sensitivity, pii, project_id, workstream_id, agent_urn)
		VALUES(?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?)`,
		r.Stream, seq, kind, r.At.UnixNano(), len(r.Payload)+64, r.Payload, sum[:], r.Class, r.PolicyID, r.Dims.Outcome,
		r.Attrs.Category, r.Attrs.Severity, r.Attrs.Sensitivity, r.Attrs.PII, r.Dims.Project, r.Dims.Workstream, r.Dims.Agent)
	if err != nil {
		return 0, err
	}
	return uint64(seq), tx.Commit()
}

func (f *fixture) Rows(stream string) []storetest.RowState {
	rows, err := f.l.r.Query(`SELECT seq, class, policy_id, at_ns, category, severity, sensitivity, pii, tier, data, COALESCE(payload_digest,''), rolled
		FROM records WHERE stream=? ORDER BY seq`, stream)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []storetest.RowState
	for rows.Next() {
		var r storetest.RowState
		var at int64
		var tier int
		if err := rows.Scan(&r.Seq, &r.Class, &r.PolicyID, &at, &r.Attrs.Category, &r.Attrs.Severity, &r.Attrs.Sensitivity, &r.Attrs.PII, &tier, &r.Payload, &r.PayloadDigest, &r.Rolled); err != nil {
			f.t.Fatal(err)
		}
		r.At, r.Tombstoned = time.Unix(0, at).UTC(), tier == 1
		out = append(out, r)
	}
	return out
}

func (f *fixture) Buckets(stream string) []storetest.Bucket {
	rows, err := f.l.r.Query(`SELECT class, bucket_ns, bucket_start, kind, category, severity, project_id, workstream_id, agent_urn, outcome, count, first_seq, last_seq
		FROM rollups WHERE stream=? ORDER BY class, bucket_ns, bucket_start`, stream)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []storetest.Bucket
	for rows.Next() {
		var b storetest.Bucket
		var ns, start int64
		if err := rows.Scan(&b.Class, &ns, &start, &b.Dims.Kind, &b.Category, &b.Severity, &b.Dims.Project, &b.Dims.Workstream, &b.Dims.Agent, &b.Dims.Outcome, &b.Count, &b.FirstSeq, &b.LastSeq); err != nil {
			f.t.Fatal(err)
		}
		b.Size, b.Start = retention.Duration(ns), time.Unix(0, start).UTC()
		out = append(out, b)
	}
	return out
}

func (f *fixture) PrunedThrough(stream string) uint64 {
	var n uint64
	if err := f.l.r.QueryRow(`SELECT pruned FROM streams WHERE stream=?`, stream).Scan(&n); err != nil && !errors.Is(err, sql.ErrNoRows) {
		f.t.Fatal(err)
	}
	return n
}

func TestRetentionStoreConformance(t *testing.T) { storetest.Run(t, newFixture) }

// The janitor on a real log: appends admitted under a policy, aged by a fake
// clock, then a pass tombstones, rolls up and trims; readers see ordinary gaps
// and tombstoned records, and duplicate ids still dedupe.
func TestJanitorOnALog(t *testing.T) {
	ctx := context.Background()
	policy, err := retention.Config{}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "log.db")
	l, err := Open(ctx, path, Options{Now: func() time.Time { return clock }, Retention: policy, RetentionPolicyID: "v1", MarkerEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ev := func(id, cat, sev string, extra string) streamhub.Event {
		return streamhub.Event{ID: id, Name: "k", Data: []byte(fmt.Sprintf(
			`{"class":{"category":%q,"severity":%q},"scope":{"project_id":"p1"},"payload":{"to":"doing"%s}}`, cat, sev, extra))}
	}
	dbg1, _ := l.Append(ctx, "env", ev("e1", "tool", "debug", ""))
	clock = clock.Add(time.Hour)
	dbg2, _ := l.Append(ctx, "env", ev("e2", "tool", "debug", ""))
	clock = clock.Add(time.Hour)
	audit, _ := l.Append(ctx, "env", ev("e3", "audit", "info", ""))
	clock = clock.Add(time.Hour)
	dbg3, _ := l.Append(ctx, "env", ev("e4", "tool", "debug", ""))
	_ = dbg1

	clock = clock.Add(20 * 24 * time.Hour)
	j := retention.NewJanitor(l.RetentionStore(), retention.NewPlanner(func(id string) (*retention.Policy, bool) { return policy, id == "v1" || id == "" }))
	rep, err := j.Run(ctx, clock)
	if err != nil {
		t.Fatal(err)
	}
	dbg := rep.Classes[retention.ClassDebug]
	if dbg == nil || dbg.Trimmed != 2 || dbg.Tombstoned < 3 || dbg.RolledUp != 3 || rep.HeldByHead != 1 || dbg.Errors != 0 {
		t.Fatalf("debug report = %+v, held %d", dbg, rep.HeldByHead)
	}
	// The head moved past the first two debug records; the audit record and the debug row behind it remain.
	head, _ := l.Head(ctx, "env")
	if head.PrunedThrough != dbg2.Seq || head.Latest != dbg3.Seq {
		t.Fatalf("head = %+v, want pruned through %d and latest %d", head, dbg2.Seq, dbg3.Seq)
	}
	var seen []streamhub.Record
	for r, err := range l.After(ctx, "env", head.PrunedThrough) {
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, r)
	}
	if len(seen) != 2 || seen[0].Seq != audit.Seq || string(seen[0].Data) == "" || seen[1].Data != nil {
		t.Fatalf("records after the trim = %+v: audit keeps its payload, the held debug row is a tombstone", seen)
	}
	// A reader behind the head gets an ordinary retention gap.
	for _, err := range l.After(ctx, "env", 0) {
		var ge *streamhub.GapError
		if !errors.As(err, &ge) || ge.Gap.Reason != streamhub.GapRetention || ge.Gap.OldestAvailable != head.PrunedThrough+1 {
			t.Fatalf("gap = %v", err)
		}
		break
	}
	// A retry of a tombstoned record is still a duplicate, not a new record.
	again, err := l.Append(ctx, "env", ev("e4", "tool", "debug", ""))
	if err != nil || again.Seq != dbg3.Seq {
		t.Fatalf("retry of a tombstoned id = %+v, %v", again, err)
	}
	// Rollups carry the payload outcome.
	var outcome string
	if err := l.r.QueryRow(`SELECT outcome FROM rollups WHERE stream='env' AND class=? LIMIT 1`, retention.ClassDebug).Scan(&outcome); err != nil || outcome != "doing" {
		t.Fatalf("rollup outcome = %q, %v", outcome, err)
	}
}
