# Migration: go-context-window to substrate/agent/contextwindow

`github.com/hollis-labs/go-context-window` moved here with 13 commits of
history from source commit `ead083babe98`. Its `v0.1.0` tag was not carried.

Replace the old import prefix with
`github.com/hollis-labs/substrate/agent/contextwindow`. Package names and public
symbols are unchanged. The old LLM dependencies now use the consolidated
`substrate/llm-core/llmtypes` and `llmcontracts` packages.
