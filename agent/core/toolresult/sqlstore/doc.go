// Package sqlstore is a [toolresult.Store] over database/sql, written for
// SQLite: it uses "?" placeholders, stores times as RFC 3339 UTC text and
// compares them as strings, and needs no driver import of its own. The default
// [Table] matches Nanite's tool_result_cache and a second [Table] value
// matches Loom's wiki_result_cache, so either app adopts it without a
// migration. [DDL] renders a CREATE TABLE for a new database.
//
// Table and column names are spliced into SQL text, so they are validated
// as plain identifiers by [New].
package sqlstore
