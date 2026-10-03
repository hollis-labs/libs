# Substrate relocation

`go-federation` now lives in the `mesh` module of the substrate repository.

| Old import prefix | New import prefix |
|---|---|
| `github.com/hollis-labs/go-federation` | `github.com/hollis-labs/substrate/mesh/federation` |

Replace the prefix for the root package and every subpackage. Package names,
APIs, and the repository's internal layout are unchanged by this move. Its
go-messaging dependency is now the sibling
`github.com/hollis-labs/substrate/mesh/messaging` package, so it no longer
requires another Hollis Labs module. The source history is preserved, but old
repository tags are not carried into the monorepo. The first consolidated
release is planned as `mesh/v0.1.0`.

No consumer import is changed as part of the relocation.
