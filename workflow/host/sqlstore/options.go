package sqlstore

import (
	"context"
	"fmt"
	"regexp"

	workflowruntime "github.com/hollis-labs/libs/workflow/runtime"
)

// RunColumns names the run table and the four columns of it the store reads
// and writes. It exists for hosts whose runs table is shared with product
// data under different names. The zero value of every field selects the
// default, which reproduces the schema Migrate creates:
//
//	Table workflow_runs, ID run_id, Status status,
//	Generation generation, CreatedAt created_at
//
// The table must also carry the store's other run columns (plan_digest,
// inputs_ref_json, outputs_ref_json, updated_at) under their default names,
// and any additional column it has must be nullable or defaulted, because the
// store's INSERT names only the columns it owns. Foreign keys from the other
// store tables must target the ID column.
type RunColumns struct {
	Table, ID, Status, Generation, CreatedAt string
}

// DefaultRunColumns is the RunColumns the store uses when WithRunColumns is
// not given.
var DefaultRunColumns = RunColumns{
	Table: "workflow_runs", ID: "run_id", Status: "status",
	Generation: "generation", CreatedAt: "created_at",
}

var sqlIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (c RunColumns) resolve() (RunColumns, error) {
	def := DefaultRunColumns
	fields := []struct {
		name  string
		value *string
		def   string
	}{
		{"Table", &c.Table, def.Table}, {"ID", &c.ID, def.ID}, {"Status", &c.Status, def.Status},
		{"Generation", &c.Generation, def.Generation}, {"CreatedAt", &c.CreatedAt, def.CreatedAt},
	}
	for _, f := range fields {
		if *f.value == "" {
			*f.value = f.def
		}
		// The names are spliced into SQL text, so they must be plain identifiers.
		if !sqlIdentifier.MatchString(*f.value) {
			return RunColumns{}, workflowInvalid(fmt.Errorf("run column %s %q is not a plain SQL identifier", f.name, *f.value))
		}
	}
	return c, nil
}

// Hooks lets a host mirror canonical run and node writes into its own tables.
// Each hook runs inside the write transaction, after the canonical statement
// that produced the snapshot succeeded; tx is the transaction's DBTX and must
// be the only handle the hook uses. A returned error rolls the whole write
// back, including the canonical statement, and is returned to the caller
// (wrap it in runtime.ErrInvalidRecord when the caller should treat it as a
// rejected record).
type Hooks interface {
	// AfterRunWritten runs after a run row is inserted or updated. The
	// snapshot is the run as now stored.
	AfterRunWritten(ctx context.Context, tx DBTX, run workflowruntime.RunSnapshot) error
	// AfterNodeWritten runs after a node invocation row (and its lease) is
	// inserted or updated. The snapshot is the node as now stored.
	AfterNodeWritten(ctx context.Context, tx DBTX, node workflowruntime.NodeInvocationSnapshot) error
}

// Option configures New.
type Option func(*Store) error

// WithRunColumns overrides the run table and column names; see RunColumns.
func WithRunColumns(columns RunColumns) Option {
	return func(s *Store) error {
		resolved, err := columns.resolve()
		if err != nil {
			return err
		}
		s.cols = resolved
		return nil
	}
}

// WithHooks installs write hooks; see Hooks. A nil value clears them.
func WithHooks(hooks Hooks) Option {
	return func(s *Store) error {
		s.hooks = hooks
		return nil
	}
}
