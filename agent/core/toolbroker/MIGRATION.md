# Migration: go-toolbroker to substrate/agent/toolbroker

`github.com/hollis-labs/go-toolbroker` moved here with 12 commits of history
from source commit `4a4c6e5cf279`. Its standalone tags through `v0.2.0` were
not carried.

Rewrite the prefix mechanically:

- `github.com/hollis-labs/go-toolbroker/broker` becomes
  `github.com/hollis-labs/substrate/agent/toolbroker/broker`.

The `broker` package and its public symbols are unchanged.
