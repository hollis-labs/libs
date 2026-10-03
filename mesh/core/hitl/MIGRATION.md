# Substrate relocation

`go-hitl` now lives in the `mesh` module of the substrate repository.

| Old import prefix | New import prefix |
|---|---|
| `github.com/hollis-labs/go-hitl` | `github.com/hollis-labs/substrate/mesh/hitl` |

Replace the prefix for the root package and every subpackage. Package names,
APIs, and the repository's internal layout are unchanged by this move. The
source history is preserved, but old repository tags are not carried into the
monorepo. The first consolidated release is planned as `mesh/v0.1.0`.

No consumer import is changed as part of the relocation.
