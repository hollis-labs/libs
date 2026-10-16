// Package util is the root package of the util module. It has no API of its own.
//
// The module's packages each live in their own directory: sqlite, sqlitebackup, svcerr,
// transportparity, strutil, otel, sftpsync, apppaths, localdaemon, queue, scheduler,
// worktree and tesseractclient. They were moved in, with their history, from separate
// modules; each directory has a MIGRATION.md with the old and the new import path.
//
// Three packages were written in this module and have no earlier path: mw (the
// middleware kit, with httpmw, jobmw and conformance below it), router (a
// declarative rule router) and ledger (the decision ledger record, with ledgertest).
//
// The module is released with module-prefixed tags of the form util/vX.Y.Z.
package util
