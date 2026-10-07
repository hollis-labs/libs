// Package layout exposes derived compatibility placement queries.
// The only authored paths, modes and launch locators live in adapters/layout/plan.
// Compatibility views retain existing adapter bytes and caller signatures;
// credential rows remain link-only with empty locators. They grant no authority.
// layout/gen generates only plan-fields.json and docs/PLAN-FIELDS.md.
// The older layout.json and docs/LAYOUT.md are retained historical evidence,
// not current generated outputs or a second placement source.
//
//go:generate go run ./gen
package layout
