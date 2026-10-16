// Package storetest is the conformance suite for [retention.Store]. A log, or
// a hub with its own tables, implements [Fixture] over its store and calls
// [Run]; [Mem] is the in-memory reference.
package storetest

import (
	"time"

	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention"
)

// Dims are the low-cardinality fields a rollup counts over, besides category
// and severity (which come from Attrs).
type Dims struct{ Kind, Project, Workstream, Agent, Outcome string }

// Row is a row to seed. Class may be empty to exercise classification.
type Row struct {
	Stream   string
	Class    string
	PolicyID string
	At       time.Time
	Attrs    retention.Attrs
	Dims     Dims
	Payload  []byte
}

// RowState is a remaining row as the store sees it.
type RowState struct {
	Seq           uint64
	Class         string
	PolicyID      string
	At            time.Time
	Attrs         retention.Attrs
	Tombstoned    bool
	Payload       []byte // nil once tombstoned
	PayloadDigest string // "sha256:<hex>" or "withheld" once tombstoned
	Rolled        bool
}

// Bucket is one rollup bucket.
type Bucket struct {
	Class              string
	Size               retention.Duration
	Start              time.Time
	Dims               Dims
	Category, Severity string
	Count              uint64
	FirstSeq, LastSeq  uint64
}

// Fixture is a store under test plus the means to seed and inspect it.
type Fixture interface {
	Store() retention.Store
	// Add seeds a row and returns its cursor.
	Add(Row) (uint64, error)
	// Rows lists the rows still in the stream, in cursor order. Trimmed rows are gone.
	Rows(stream string) []RowState
	// Buckets lists the rollup buckets of the stream.
	Buckets(stream string) []Bucket
	// PrunedThrough is the stream's pruned-through cursor (head-only stores).
	PrunedThrough(stream string) uint64
}
