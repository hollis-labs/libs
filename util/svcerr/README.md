# go-svcerr

Small transport-agnostic typed error carrier: status, machine code, safe message, with errors.Is/As support.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

See [CHANGELOG.md](./CHANGELOG.md) for what exists and what changed.

## Install

```sh
go get github.com/hollis-labs/go-svcerr
```

## Usage

```go
package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	svcerr "github.com/hollis-labs/go-svcerr"
)

func main() {
	// A service returns a typed error: a code, a safe message, and (kept
	// server-side) the internal cause.
	cause := errors.New("sql: no rows in result set")
	err := fmt.Errorf("tasks: %w", svcerr.Wrap(cause, svcerr.CodeNotFound, "no such task"))

	// Callers match on the category, however deep it is wrapped.
	fmt.Println(errors.Is(err, svcerr.ErrNotFound)) // true

	// A handler derives the status from the error instead of guessing it.
	fmt.Println(svcerr.StatusFor(err, http.StatusInternalServerError)) // 404

	// Optional: write a JSON body. Only the safe message is ever sent.
	rec := httptest.NewRecorder()
	svcerr.WriteJSON(rec, err, http.StatusInternalServerError)
	fmt.Print(rec.Body.String()) // {"error":{"code":"not_found","message":"no such task"}}
}
```

## The carrier and the envelope

`Error` carries `Status`, `Code`, a safe `Message`, an optional `Field` (the offending input) and an internal `Err`. `New` and `Wrap` build one; `WithField` and `WithStatus` adjust it. There are six built-in codes (`invalid` 400, `not_found` 404, `conflict` 409, `permission` 403, `unavailable` 503, `internal` 500) and `Code` is an open string, so an app can add its own and give it a status with `WithStatus`. `errors.Is(err, svcerr.ErrNotFound)` matches on the code, whatever the message or status.

- `Message` is never taken from a wrapped error. It is an explicit string you chose to show. `Err` is for logging.
- `StatusFor` and `CodeFor` read the first `*Error` in the chain. For any other error they return your fallback, or the empty code. They never look at the error's text.
- `Status` is never zero or invalid on the way out: an unset or out-of-range status falls back to the code's default, then to 500, because `http.ResponseWriter.WriteHeader` panics outside 100..999.

`WriteJSON` and `Envelope` are one **optional** default for a new handler that has no error envelope yet, in `json.go`, and nothing else depends on them. The body is `{"error":{"code","message","field"?}}`, the nesting Tether already uses. For a non-`*Error` it writes your fallback status and a generic `internal error` body, and never the error's own text. An app with an existing wire shape keeps it and adopts only `Error`, `StatusFor` and `CodeFor` underneath.

## Compatibility

This module is pre-1.0: minor releases may break the exported API. Pin an exact
version, and read [CHANGELOG.md](./CHANGELOG.md) before upgrading; every
breaking change is listed there. The optional JSON envelope is a default for new
handlers, not a wire contract any existing app is asked to adopt.

## Out of scope

- A mandated wire format. Nine apps write six different JSON shapes today; this package does not pick one for them.
- Redaction or scoping of error detail (Cerberus keeps its own).
- JSON-RPC or A2A error types: a different wire protocol from an HTTP status carrier. A follow-up if a real second JSON-RPC adopter asks.
- Deriving a status or code from an error's text.
- Any dependency beyond the standard library.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
