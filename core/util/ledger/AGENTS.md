# ledger

The decision ledger record, its events, retention shaping and the store contract.

It is not a database or a service. A change that adds a driver, a network call or a global to this package is the wrong change; implementations of `Store` live elsewhere and pass `ledgertest.Run`.

## Start Here

- `ledger.go` — `Record`, `Event`, `Entry`, `Key`, `Store`, `Expirer`, validation and the disposition lifecycle.
- `retention.go` — `Retention.Shape` and the per-stage rules.
- `mem.go` — `MemStore`. `ledgertest/` — the conformance suite.

## Commands

```sh
export GOWORK=off
scripts/check -race core/util
```

## Boundaries

- Standard library only; no import of any other module package except from tests.
- A record is immutable once appended; changes are events. Guarded by the `Store` contract in `ledgertest`.
- `Append` is idempotent on the key, including under concurrency. Guarded by `ledgertest` ("concurrent duplicates write one row").
- A disposition moves one way and a repeated move is refused. Guarded by `ledgertest` ("disposition moves one way").
- A correction needs a label and an actor, and never rewrites the original output. Guarded by `ledgertest` ("corrections are labels, originals untouched").
- A failing redactor refuses the write; raw input is never stored instead. Guarded by `TestRetentionShape`.
- Expiry drops only the snapshot, never the hash or any other field, and never a Keep row. Guarded by `TestExpireKeepsEverythingButTheSnapshot`.
- Returned values are copies; mutating them does not change the store. Guarded by `ledgertest` ("returned values are copies").
- The key is length-prefixed so field boundaries cannot collide. Guarded by `TestKeyIsPerSubjectStageVersion`.
