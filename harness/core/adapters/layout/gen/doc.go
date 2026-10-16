// Command gen renders the sole plan-field table into adapters/layout/docs/PLAN-FIELDS.md
// and adapters/layout/plan-fields.json, relative to the module root. Historical
// LAYOUT.md and layout.json are retained evidence and are neither rewritten nor checked.
//
//	go run ./adapters/layout/gen          write both current files
//	go run ./adapters/layout/gen -check   exit 1 when either current file is stale
//
// The module root is found by walking up from the working directory to go.mod;
// -root overrides it.
package main
