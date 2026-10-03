// Package workflow is the root package of the workflow module. It has no API of its own.
//
// The workflow engine's packages (graph, compile, runtime, wait, gate and the others) sit
// beneath it, optional adapters are in adapters/, and the host layer (artifactfs, sqlstore)
// is in host/. The packages were moved in, with their history, from the separate go-workflow
// and go-workflow-host modules; MIGRATION.md and host/MIGRATION.md give the old and the new
// import paths.
//
// The module is released with module-prefixed tags of the form workflow/vX.Y.Z.
package workflow
