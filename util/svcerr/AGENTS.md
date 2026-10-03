# go-svcerr

Small transport-agnostic typed error carrier: status, machine code, safe message, with errors.Is/As support.

It is not an HTTP framework, a redaction layer or a wire-format standard: it is the shared carrier underneath the handler layers apps already have. A change that makes it choose a JSON shape for existing apps, render an internal error into a response, or guess a status from text is the wrong change.

## Start Here

- `svcerr.go` — `Error`, the codes and their default statuses, the sentinels, `StatusFor` and `CodeFor`. The whole contract.
- `json.go` — the OPTIONAL `WriteJSON` and `Envelope`. Nothing in `svcerr.go` may depend on it.
- `doc.go` — the package documentation and the two rules the package holds to.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either. Standard library only.
- `Message` is never derived from `Err`, and `Error()` and `WriteJSON` never render `Err`. Guarded by `TestTheInternalCauseIsReachableButNeverRendered` and `TestWriteJSONNeverLeaksAPlainErrorsText`.
- A status is never guessed from error text: a non-`*Error` gets the caller's fallback. Guarded by `TestStatusForAndCodeForDoNotGuessFromText`.
- A status handed to `WriteHeader` is always an error status, 400..599 (`WriteHeader` panics outside 100..999, and a success or redirect status would report a failure as success; a 204 cannot carry the body at all), and the sentinels carry their code's status so returning one directly is safe. Guarded by `TestStatusIsNeverZeroOrInvalid`, `TestNonErrorStatusesFallBackToTheCodesDefault`, `TestWriteJSONNeverWritesAnInvalidStatus` and `TestWriteJSONRefusesASuccessFallbackAndStillWritesTheBody`.
- `Is` matches on `Code` only, so `errors.Is(err, ErrNotFound)` works through wrapping. Guarded by `TestIsMatchesThroughWrappingLayers`.
- The sentinels are shared pointers: never modify them.
- `Code` stays an open string type; do not turn it into a closed enum.
- `WriteJSON`'s nesting deliberately matches Tether's existing `{"error":{"code","message"}}`; no existing app is asked to change its wire format.
