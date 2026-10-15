// Package sqlstore is a SQLite-backed implementation of go-workflow's
// runtime.StateStore and every companion runtime store interface (waits,
// memoization, recovery, compensation, scheduler resources, and the rest; see
// assertions.go for the full list).
//
// It is stdlib database/sql plus go-workflow only: the caller opens the
// database with the SQLite driver of its choice, applies the schema with
// Migrate (or applies Schema through its own migration runner), and hands the
// *sql.DB to New. The package never imports a driver outside its tests.
//
//	db, _ := sql.Open("sqlite", "workflow.db") // any database/sql SQLite driver
//	db.SetMaxOpenConns(1)
//	_ = sqlstore.Migrate(ctx, db)
//	store, _ := sqlstore.New(db)
//	var _ runtime.StateStore = store
//
// The SQL is SQLite dialect (BEGIN IMMEDIATE, json_extract, ? placeholders).
// A different database is a second implementation qualified by the same
// go-workflow conformance suite, not a dialect switch here.
//
// Hosts that keep their own tables in the same database can run statements in
// the store's write transaction with Store.WriteTx and the DBTX it supplies.
package sqlstore
