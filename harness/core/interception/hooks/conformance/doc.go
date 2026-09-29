// Package conformance ships the go-hooks conformance fixtures and a
// reference runner for command hooks.
//
// The fixture tree (embedded as [FixtureFS], rooted at "testdata") holds one
// directory per case at testdata/<Event>/<case>/ with three files:
//
//	hook.sh     a real executable script: stdin JSON in, stdout JSON out,
//	            exit code as the signal
//	input.json  the JSON a host writes to the hook's stdin
//	want.json   {"exit_code":N,"output":{...}|null,"want_err_substr":"..."}
//
// [Load] reads the tree into [Case] values and [Run] executes each case for
// real through a [cmdhook.Runner], so a Go host proves its dispatch against
// the same fixtures. A host in any other language re-implements the same
// loop against the same tree, which travels with this module.
//
// An embedded file system cannot carry an executable bit, so [Run] writes
// each script to a temporary directory with mode 0755 before executing it.
// The scripts are also committed with the executable bit set so the tree can
// be run in place from a checkout.
package conformance
