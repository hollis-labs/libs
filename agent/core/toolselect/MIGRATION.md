# Migration: go-toolselect to substrate/agent/toolselect

`github.com/hollis-labs/go-toolselect` moved here with 14 commits of history
from source commit `bcc8d3389b11`. Its standalone tags through `v0.3.0` were
not carried.

Replace the old prefix with
`github.com/hollis-labs/substrate/agent/toolselect`; the `profile` and `launch`
suffixes remain unchanged. `launch` now imports
`github.com/hollis-labs/substrate/llm-core/contracts` under its existing
`contracts` alias. Package names and public symbols are unchanged.
