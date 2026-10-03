// Package util is the root package of the util module. It has no API of its own.
//
// The module's packages each live in their own directory: sqlite, sqlitebackup, svcerr,
// transportparity, strutil, otel, sftpsync, apppaths, localdaemon, queue, scheduler and
// worktree. They were moved in, with their history, from separate modules; each
// directory has a MIGRATION.md with the old and the new import path.
//
// The module is released with module-prefixed tags of the form util/vX.Y.Z.
package util
