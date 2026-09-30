# go-tesseract-client

Small shared HTTP client for Tesseract's memory API: recall, deprecate, point-reads, namespace listing and health.

It is not: an MCP client, a general Tesseract SDK, or a place to add operations "while we're here". It wraps exactly the HTTP calls Station's and Tangent's clients make; everything else Tesseract can do is out of scope (see the README).

## Start Here

- `tesseract` package — the importable API; its `doc.go` is the package documentation.
- `client.go`, `types.go`, `errors.go` — the client, wire types, and error mapping.
- `tesseracttest/` — the importable fake Tesseract; also this library's own test server.
- `filters_test.go` — the hand-copied server filter key list, with the server file:line it mirrors.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- **`RecallFilters` mirrors the server's real fields and never grows one the server would reject.** The recall route decodes with `DisallowUnknownFields`, so an invented field (Tangent's old `Origins`) turns every recall that sets it into a 400. Keys are capitalized Go field names, not snake_case, because the server decodes `filters` into an untagged struct. Guarded by `TestFiltersEncodeWithTheServersGoFieldNames`, `TestRecallFiltersFieldSetMirrorsTheServer` (against the hand-copied key list in `filters_test.go`; update that list from `apps/tesseract` recall.go and memory_handler.go when the server changes) and `TestLegacyFilterFieldsCannotBeSent`. Do not import the tesseract app to check this.
- **HTTP only.** No MCP transport and no operations beyond the README's list.
- **Stdlib only.** `go list -deps ./...` must show no third-party module; `go.sum` must not exist.
- **The response-size cap is enforced, not a truncating read.** An over-cap body is `ErrResponseTooLarge` (also `ErrUnavailable`), never decoded. Guarded by `TestOverCapResponseIsAnErrorNotATruncatedDecode`.
- **Error mapping is Station's.** `ErrUnavailable` is transport, read, oversize and decode failures only; 5xx statuses are `*APIError`. `errors.Is(err, ErrNotFound)` holds for 404 or code `not_found`. Guarded by `TestA404MatchesErrNotFound` and `TestServerErrorStatusesAreAPIErrorsNotUnavailable`. Station matches `ErrUnavailable`, so do not widen it without checking adopters.
- **Auth is optional and static.** A non-empty token sends `Authorization: Bearer <token>`; an empty token sends no header at all. Guarded by `TestBearerTokenIsSentOnlyWhenSet` and `TestBearerTokenAgainstAServerThatRequiresOne`.
- **`tesseracttest` decodes as strictly as the server** (unknown recall or filter keys are a 400). Loosening it hides exactly the drift this library exists to prevent.
- No retries or backoff, and no untested new exported surface: every exported entry point has an `Example*`.
- Paging loops (`RecallAll`, `ListNamespaces`) track every cursor they have used and fail on a repeat, and `ListNamespaces` has a page cap. Comparing only with the previous cursor is not enough (an A, B, A cycle got past it). Guarded by `TestRecallAllRefusesACursorCycle`, `TestListNamespacesRefusesACursorCycle` and `TestListNamespacesStopsAServerThatNeverEnds`.
