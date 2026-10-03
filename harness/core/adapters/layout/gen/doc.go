// Command gen renders the layout table into adapters/layout/docs/LAYOUT.md
// (human table) and adapters/layout/layout.json (for non-Go readers such as
// Cairn layouts and agent-launcher), both relative to the module root. It is
// run by "go generate ./..." from package layout.
//
//	go run ./adapters/layout/gen          write both files
//	go run ./adapters/layout/gen -check   exit 1 when either file is stale
//
// The module root is found by walking up from the working directory to go.mod;
// -root overrides it.
package main
