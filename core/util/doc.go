// Package util is the root package of the util module. It has no API of its own.
//
// The module's packages each live in their own directory: sqlite, sqlitebackup, svcerr,
// transportparity, strutil, otel, sftpsync, apppaths, localdaemon, queue, scheduler,
// worktree and tesseractclient. They were moved in, with their history, from separate
// modules; each directory has a MIGRATION.md with the old and the new import path.
//
// Seven packages were written in this module and have no earlier path: mw (the
// middleware kit, with httpmw, jobmw and conformance below it), router (a
// declarative rule router), ledger (the decision ledger record, with ledgertest),
// classify (facet classification with judges, packs and an abstaining engine),
// decide (structured questions to a decision provider), embed (texts to vectors
// through an AI gateway) and workclient (a client for the Work service's HTTP API).
//
// The module is released with module-prefixed tags of the form core/util/vX.Y.Z.
package util
