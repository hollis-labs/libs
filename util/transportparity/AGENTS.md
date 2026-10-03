# go-transportparity

Test helpers for asserting that HTTP, MCP and ACP doors fronting one operation agree on outcome.

It is not a parity framework, a surface catalog or a production dependency: it is a few small assertions that a test calls after running each door itself. A change that makes it invoke a transport, keep a per-app catalog or waiver list, or compare wire shape or status numbers is almost certainly the wrong change.

## Start Here

- `outcome.go` — `Outcome`, `FromValue`, `FromError`, `AssertSameOutcome` and the small `T` interface the assertions take.
- `canonical.go` — `CanonicalJSON`.
- `httpjson.go` — `HTTPJSON`.
- `fields.go` — `AcceptedFieldNames` and `AssertSameFieldNames`.
- `doc.go` — what counts as agreement, and what is deliberately absent.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`. The one non-stdlib dependency is go-svcerr, deliberately.
- Agreement is outcome, not shape: status numbers and error wording are never compared (`TestStatusAndDetailAreNeverCompared`), and a failure with no category is rejected because it cannot be compared (`TestUncategorizedFailuresAreRejected`).
- `CanonicalJSON` is not the marshaled bytes: it must sort keys at every depth and normalise numbers (`TestCanonicalJSONIsNotByteForByte`, `TestCanonicalJSONNormalizesNumbers`), while keeping types and array order distinct (`TestCanonicalJSONKeepsTypesApart`).
- `AcceptedFieldNames` follows what `encoding/json` actually does: it flattens untagged embedded structs, descends only into types that serialize as objects, and returns nil for nil rather than panicking. `TestAcceptedFieldNamesMatchesWhatEncodingJSONEmits` is the ground truth; a change to the walker that this test does not like is wrong.
- `HTTPJSON` calls the handler directly with a recorder (no listener), and reports a non-JSON body rather than comparing it.
- The assertions take the small `T` interface, not `*testing.T`, so the package can test its own failure paths; `*testing.T` and `testing.TB` satisfy it.
