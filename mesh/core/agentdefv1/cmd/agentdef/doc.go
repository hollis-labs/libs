// Command agentdef checks v1 agent definition files: validate, lint, digest
// and check (stale generated spans).
//
// Usage:
//
//	agentdef validate <path>...
//	agentdef lint [--strict] <path>...
//	agentdef digest [--skills] <path>...
//	agentdef check <path>...
//
// Every subcommand also accepts --layer root=<dir>,precedence=<int>
// (repeatable) to load a whole directory as one merged layer instead of
// naming files. Errors print one per line as "<path>: <field>: <message>" on
// stderr; the exit code is 0 on success, 1 when a check failed, 2 on bad
// usage. There is deliberately no --force: overriding an unmet requirement is
// a launch-time decision for a host, not this tool.
package main
