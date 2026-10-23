// Package search is portable full-text search: one index definition and one
// query model, implemented with FTS5 on SQLite and with a generated tsvector
// column (GIN-indexed, optionally pg_trgm) on Postgres.
//
// # Shape
//
// An [Index] names a source table, its id column, the text columns to search
// (with weights) and the columns results may be filtered on. The index is
// "external content": the text stays in the source table and the search
// structure is derived from it, so there is no second copy to keep in sync by
// hand (SQLite uses triggers; Postgres uses a stored generated column).
//
// [Index.Up] and [Index.Down] emit the DDL for a migration file, one list per
// dialect. [Index.GooseSQL] renders a ready goose file body.
//
// A [Searcher] runs [Query] values and returns [Hit] values: the id (as text),
// a score where higher is better, and an optional highlighted snippet.
//
// # Queries
//
// A query is an [Expr] tree (terms, phrases, prefixes, AND, OR, NOT, field
// filters) or plain user text turned into one by [Parse]. Text is never passed
// to the engine: terms are quoted and escaped for FTS5 and tsquery alike, so
// operators and punctuation in user input cannot change the query's structure,
// and malformed input yields fewer or no hits, never an error.
//
// NOT is a filter, not a standalone match: it must sit in an AND with at least
// one positive sibling.
//
// # What is and is not the same on both engines
//
// The result SET for ordinary word, phrase, prefix, AND/OR/NOT and field
// queries is the same; the conformance suite asserts sets and top-k
// containment. These differ by design and are accepted:
//
//   - Scores are comparable within one dialect only (bm25 on SQLite, ts_rank_cd
//     on Postgres), and ranking order differs.
//   - Tokenisation: SQLite uses the unicode61 tokenizer; Postgres uses the
//     'simple' text-search configuration. Hyphenated words, URLs, e-mail
//     addresses and numbers with separators split differently. No stemming by
//     default on either; set [Index.Language] on Postgres to opt in there (SQLite
//     has no equivalent, so the dialects then diverge).
//   - Diacritics are folded on both by default (unaccent on Postgres).
//   - Snippets: SQLite snippet() and Postgres ts_headline() choose different
//     fragments. On Postgres with diacritic folding the snippet text is the
//     folded text.
//   - Fuzzy and substring matching ([Query.Substring]) are LIKE based on both;
//     Postgres accelerates them with a trigram index when [Index.Fuzzy] is set.
package search
