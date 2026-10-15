# Migration: go-loopdetect to substrate/agent/loopdetect

`github.com/hollis-labs/go-loopdetect` moved here with 5 commits of history
from source commit `d4f5d70ad1ea`. Its `v0.1.0` tag was not carried.

Replace the old import prefix with
`github.com/hollis-labs/substrate/agent/loopdetect`. The `loopdetect` package,
its public symbols and its standard-library-only dependency boundary are
unchanged.
