// Package migrate runs a module's schema migrations on either engine, on top of
// goose v3 (provider API). go-store adds what goose does not: per-dialect source
// directories checked for parity, a startup verify that needs no DDL rights, an
// auto|verify mode, and a baseline helper for apps adopting go-store with an
// existing custom version table.
//
// # Layout
//
// An app embeds one tree with a directory per dialect:
//
//	migrations/sqlite/0001_init.sql
//	migrations/postgres/0001_init.sql
//
//	//go:embed migrations
//	var FS embed.FS
//	var Source = migrate.Source{FS: FS, Root: "migrations"}
//
// Files are goose SQL migrations ("-- +goose Up" / "-- +goose Down"). Version
// numbers must be identical in both directories: [CheckParity], run from a unit
// test, enforces it. Statements containing semicolons (triggers, functions)
// need goose's StatementBegin/StatementEnd markers.
//
// # Credentials
//
// [Up] applies migrations over Config.Postgres.MigrateDSN when set (the schema
// owner) and [Verify] checks the version over the runtime connection (DML-only).
// [Ensure] does both according to Config.SchemaMode. Concurrent migrators on
// Postgres are serialised by a goose advisory lock.
package migrate
