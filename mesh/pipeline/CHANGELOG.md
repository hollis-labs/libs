# Changelog

## v0.1.0

- Module path is now `github.com/hollis-labs/libs/mesh/pipeline` (was `github.com/hollis-labs/libs/message-pipeline`); the previous path and its `message-pipeline/vX.Y.Z` tags no longer exist. Tags are `mesh/pipeline/vX.Y.Z`. No code change besides the path.
- Add portable immutable message/stage contracts, ordered fail-open runner,
  bounded typed annotations/traces and durable result/cursor store interfaces.
- Preserve shutdown pending work and exact replay outcomes without claiming
  provider exactly-once or owning adapter effects and SQL transactions.
