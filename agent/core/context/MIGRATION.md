# Migration to agent/core/context

`github.com/hollis-labs/go-context-window` moved here with 13 commits of
history from source commit `ead083babe98`. Its `v0.1.0` tag was not carried.

The canonical package is `github.com/hollis-labs/libs/agent/core/context`.
Replace imports of `github.com/hollis-labs/libs/agent/core/contextwindow`
(or the earlier standalone package) with that path. Its package name is
`context`; use an explicit alias such as `ctxpkg` when also importing the
standard library's `context`. There are no aliases or re-exports at the old
path.

The canonical package retains the exported `NamedStage`, opaque random
compaction event identifiers and mechanism tests from `contextwindow`.
`SlotOrder`, default budgets and compactability, agent-inline handling,
lazy-load handling and the four compaction stages keep their existing
behavior. Consumers of the earlier `context` implementation must treat
compaction event IDs as opaque strings rather than UUIDs.

Release the changed `agent/core` module before adopting it in a consumer.
Then use `go get github.com/hollis-labs/libs/agent/core@<released-version>`
and migrate imports together. Keep the consumer's existing usable pin until
that release is resolvable; a local replacement is development evidence only.
Run the consumer's assembly, cache-marker, handoff and compaction tests against
the released dependency. Earlier pinned releases remain usable independently.

LLM dependencies use `github.com/hollis-labs/libs/llm/core/llmtypes` and
`llmcontracts`.
