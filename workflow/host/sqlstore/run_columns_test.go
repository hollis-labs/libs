package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/workflow/conformance"
	workflowruntime "github.com/hollis-labs/libs/workflow/runtime"
)

// naniteRunColumns is the run-table shape of a host that shares workflow_runs
// with product data (Nanite): renamed identity/status/generation/created
// columns, extra product columns that are NOT NULL, defaulted or CHECKed.
var naniteRunColumns = RunColumns{
	Table: "workflow_runs", ID: "id", Status: "runtime_status",
	Generation: "runtime_generation", CreatedAt: "started_at",
}

const naniteRunsDDL = `
CREATE TABLE workflow_runs (
    id TEXT PRIMARY KEY,
    definition_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'running'
        CHECK(status IN ('running','completed','failed','cancelled','waiting_on_gate')),
    input_json TEXT NOT NULL DEFAULT '{}',
    error TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    completed_at TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT '',
    engine_kind TEXT NOT NULL DEFAULT 'host_v1',
    plan_digest TEXT REFERENCES workflow_plan_refs(digest),
    runtime_status TEXT,
    inputs_ref_json TEXT,
    outputs_ref_json TEXT,
    runtime_generation INTEGER NOT NULL DEFAULT 0 CHECK (runtime_generation >= 0)
);
CREATE INDEX idx_workflow_runs_runtime_recovery ON workflow_runs(runtime_status, started_at, id);
CREATE TABLE product_node_projection (
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    status TEXT NOT NULL,
    PRIMARY KEY (run_id, node_id)
);
`

// naniteShapedSchema derives the fixture schema from the embedded baseline:
// the store's own workflow_runs table is replaced by naniteRunsDDL and every
// foreign key into it is retargeted at the renamed identity column.
func naniteShapedSchema(t testing.TB) string {
	t.Helper()
	body, err := fs.ReadFile(Schema(), "0001_workflow_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	schema := string(body)
	table := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS workflow_runs \(.*?\n\);\n`)
	index := regexp.MustCompile(`(?s)CREATE INDEX IF NOT EXISTS idx_workflow_runs_recovery.*?;\n`)
	if !table.MatchString(schema) || !index.MatchString(schema) {
		t.Fatal("baseline schema no longer contains the expected workflow_runs definition")
	}
	schema = table.ReplaceAllString(schema, naniteRunsDDL)
	schema = index.ReplaceAllString(schema, "")
	return strings.ReplaceAll(schema, "REFERENCES workflow_runs(run_id)", "REFERENCES workflow_runs(id)")
}

// naniteProjection mirrors canonical writes into product columns and a product
// node table, the way a host's projection layer would.
type naniteProjection struct {
	failRun  func(workflowruntime.RunSnapshot) error
	failNode func(workflowruntime.NodeInvocationSnapshot) error
}

func (p naniteProjection) AfterRunWritten(ctx context.Context, tx DBTX, run workflowruntime.RunSnapshot) error {
	if p.failRun != nil {
		if err := p.failRun(run); err != nil {
			return err
		}
	}
	status := map[workflowruntime.RunStatus]string{
		workflowruntime.RunPending: "running", workflowruntime.RunRunning: "running",
		workflowruntime.RunSucceeded: "completed", workflowruntime.RunFailed: "failed",
		workflowruntime.RunTimedOut: "failed", workflowruntime.RunCrashed: "failed",
		workflowruntime.RunCanceled: "cancelled", //nolint:misspell // the host product CHECK constraint spells it this way
		workflowruntime.RunWaiting:  "waiting_on_gate",
	}[run.Status]
	if status == "" {
		return workflowInvalid(fmt.Errorf("unsupported run status projection %q", run.Status))
	}
	_, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status = ?, definition_name = ? WHERE id = ?`, status, run.Plan.ID, run.ID)
	return err
}

func (p naniteProjection) AfterNodeWritten(ctx context.Context, tx DBTX, node workflowruntime.NodeInvocationSnapshot) error {
	if p.failNode != nil {
		if err := p.failNode(node); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO product_node_projection(run_id, node_id, status) VALUES (?, ?, ?)
ON CONFLICT(run_id, node_id) DO UPDATE SET status = excluded.status`, node.ID.RunID, node.ID.NodeID, node.Status)
	return err
}

func openNaniteShaped(t testing.TB, path string, hooks Hooks) (*testDB, *Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err = db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	if _, err = db.ExecContext(ctx, naniteShapedSchema(t)); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("apply nanite-shaped schema: %w", err)
	}
	store, err := New(db, WithRunColumns(naniteRunColumns), WithHooks(hooks))
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return &testDB{db: db}, store, nil
}

func TestRunColumnsHooksRoundTrip(t *testing.T) {
	d, state, err := openNaniteShaped(t, filepath.Join(t.TempDir(), "nanite.db"), naniteProjection{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	at := workflowTestTime()
	run := createWorkflowTestRun(t, state, "run-nanite", at)
	loaded, err := state.LoadRun(ctx, run.ID)
	if err != nil || loaded.ID != run.ID || loaded.Status != workflowruntime.RunPending || loaded.Generation != run.Generation || !loaded.CreatedAt.Equal(at) {
		t.Fatalf("LoadRun = %#v, %v; want %#v", loaded, err, run)
	}
	var product, canonical, definition, started string
	var generation int
	query := `SELECT status, runtime_status, definition_name, started_at, runtime_generation FROM workflow_runs WHERE id = ?`
	if err = d.db.QueryRow(query, run.ID).Scan(&product, &canonical, &definition, &started, &generation); err != nil {
		t.Fatal(err)
	}
	if product != "running" || canonical != "pending" || definition != run.Plan.ID || started != workflowTime(at) || generation != int(run.Generation) { //nolint:gosec // G115: small test generation
		t.Fatalf("run columns = %q %q %q %q %d", product, canonical, definition, started, generation)
	}

	node := createWorkflowTestNode(t, state, run.ID, "work", at)
	var nodeStatus string
	if err = d.db.QueryRow(`SELECT status FROM product_node_projection WHERE run_id = ? AND node_id = ?`, run.ID, node.ID.NodeID).Scan(&nodeStatus); err != nil || nodeStatus != string(node.Status) {
		t.Fatalf("node projection = %q, %v", nodeStatus, err)
	}
	if _, err = state.TransitionNode(ctx, workflowruntime.NodeTransitionRequest{InvocationID: node.ID, ExpectedGeneration: node.Generation, To: workflowruntime.NodeReady, At: at}); err != nil {
		t.Fatal(err)
	}
	if err = d.db.QueryRow(`SELECT status FROM product_node_projection WHERE run_id = ? AND node_id = ?`, run.ID, node.ID.NodeID).Scan(&nodeStatus); err != nil || nodeStatus != string(workflowruntime.NodeReady) {
		t.Fatalf("node projection after update = %q, %v", nodeStatus, err)
	}

	running, err := state.TransitionRun(ctx, workflowruntime.RunTransitionRequest{RunID: run.ID, ExpectedGeneration: run.Generation, To: workflowruntime.RunRunning, At: at.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.TransitionRun(ctx, workflowruntime.RunTransitionRequest{RunID: run.ID, ExpectedGeneration: running.Snapshot.Generation, To: workflowruntime.RunSucceeded, At: at.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err = d.db.QueryRow(query, run.ID).Scan(&product, &canonical, &definition, &started, &generation); err != nil {
		t.Fatal(err)
	}
	if product != "completed" || canonical != "succeeded" {
		t.Fatalf("after transitions product=%q canonical=%q", product, canonical)
	}
}

func TestHookErrorRollsBackCanonicalWrite(t *testing.T) {
	rejected := errors.New("projection rejected")
	hooks := naniteProjection{
		failRun: func(run workflowruntime.RunSnapshot) error {
			if run.ID == "run-reject" {
				return workflowInvalid(rejected)
			}
			return nil
		},
		failNode: func(node workflowruntime.NodeInvocationSnapshot) error {
			if node.ID.NodeID == "reject-node" {
				return workflowInvalid(rejected)
			}
			return nil
		},
	}
	d, state, err := openNaniteShaped(t, filepath.Join(t.TempDir(), "rollback.db"), hooks)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	at := workflowTestTime()

	_, _, err = state.CreateRun(ctx, workflowruntime.CreateRunRequest{
		ID: "run-reject", Plan: workflowTestPlan("run-reject"), Status: workflowruntime.RunPending,
		StartIdempotencyKey: "start-run-reject", CreatedAt: at,
	})
	if !errors.Is(err, workflowruntime.ErrInvalidRecord) || !errors.Is(err, rejected) {
		t.Fatalf("CreateRun with failing hook error = %v", err)
	}
	for _, table := range []string{"workflow_runs", "workflow_run_start_idempotency", "workflow_plan_refs"} {
		var count int
		if err = d.db.QueryRow(`SELECT COUNT(1) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rows after rolled-back CreateRun = %d, %v", table, count, err)
		}
	}
	if _, err = state.LoadRun(ctx, "run-reject"); !errors.Is(err, workflowruntime.ErrNotFound) {
		t.Fatalf("LoadRun after rollback error = %v", err)
	}

	run := createWorkflowTestRun(t, state, "run-ok", at)
	_, err = state.CreateNodeInvocation(ctx, workflowruntime.CreateNodeInvocationRequest{Snapshot: workflowruntime.NodeInvocationSnapshot{
		ID:     workflowruntime.NodeInvocationID{RunID: run.ID, NodeID: "reject-node"},
		Status: workflowruntime.NodePending, CreatedAt: at, UpdatedAt: at,
	}})
	if !errors.Is(err, rejected) {
		t.Fatalf("CreateNodeInvocation with failing hook error = %v", err)
	}
	var count int
	if err = d.db.QueryRow(`SELECT COUNT(1) FROM workflow_node_invocations WHERE node_id = 'reject-node'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("node rows after rolled-back create = %d, %v", count, err)
	}
}

func TestWithRunColumnsRejectsNonIdentifiers(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ident.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, columns := range []RunColumns{
		{ID: "id; DROP TABLE workflow_runs"}, {Table: "runs r"}, {Status: `"status"`}, {CreatedAt: "a-b"},
	} {
		if _, err = New(db, WithRunColumns(columns)); !errors.Is(err, workflowruntime.ErrInvalidRecord) {
			t.Fatalf("New(%+v) error = %v, want invalid record", columns, err)
		}
	}
	if _, err = New(db, WithRunColumns(RunColumns{})); err != nil {
		t.Fatalf("zero RunColumns must select defaults: %v", err)
	}
}

func TestRunExhaustiveOnHostRunColumnShape(t *testing.T) {
	t.Parallel()
	conformance.RunExhaustive(t, conformance.EmbeddedFixtures(), storeConformanceHost{t: t, open: func(path string) (*testDB, *Store, error) {
		return openNaniteShaped(t, path, naniteProjection{})
	}})
}
