# go-tesseract-client

Small shared HTTP client for Tesseract's memory API: recall, deprecate, point-reads, namespace listing and health.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/libs/util/tesseractclient
```

There is no published release yet; until one exists, depend on a commit.

## Usage

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	tesseract "github.com/hollis-labs/libs/util/tesseractclient"
)

func main() {
	// "" means http://127.0.0.1:8089; an empty token sends no Authorization header.
	c := tesseract.New("", "")
	ctx := context.Background()

	if err := c.Health(ctx); err != nil {
		log.Fatalf("tesseract is not answering: %v", err)
	}

	page, err := c.Recall(ctx, tesseract.RecallRequest{
		Namespaces: []string{"project/example/knowledge/*"},
		Ranking:    "chronological",
		Filters:    tesseract.RecallFilters{Statuses: []string{"canonical"}},
		Limit:      20,
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range page.Results {
		fmt.Println(r.Revision.MemoryKey, r.Revision.Payload.Summary)
	}
	if page.Manifest.Truncated {
		fmt.Println("more available; use RecallAll or follow", page.Manifest.NextCursor)
	}

	_, err = c.GetCurrent(ctx, "project/example/knowledge/notes", "missing")
	switch {
	case errors.Is(err, tesseract.ErrNotFound):
		fmt.Println("no such record")
	case errors.Is(err, tesseract.ErrUnavailable):
		fmt.Println("tesseract is down")
	}
}
```

Errors from a non-2xx answer are `*tesseract.APIError` values carrying
Tesseract's own code and message. `ErrNotFound` matches a 404 or a `not_found`
code; `ErrUnavailable` matches transport failures, unreadable or oversize
bodies, and undecodable 2xx bodies (5xx statuses are `APIError`s and match
neither). Defaults are a 20s request timeout and a 64 MiB response cap,
overridable with `WithTimeout` and `WithMaxResponseBytes`; a response over the
cap is an error, never a truncated decode.

For tests, [`tesseracttest`](./tesseracttest) is an importable fake Tesseract
that implements these routes, counts and records requests, and can be made to
fail, demand a token, or page.

## Compatibility

This library wraps Tesseract's HTTP surface, which is itself pre-1.0: Tesseract
records in its own changelog that breaking changes can land in any minor
release. This module tracks that instability rather than promising more than
the wrapped service does. It is pre-1.0; any release may break the exported
API to follow Tesseract, there is no 1.0 promise, and pinning an exact version
and reading [CHANGELOG.md](./CHANGELOG.md) before upgrading is the supported
way to consume it. `RecallFilters` deliberately carries only fields the
server's recall route accepts today (see AGENTS.md).

## Out of scope

- The MCP transport: HTTP only.
- Operations neither Station nor Tangent calls: no memory/knowledge/workspace/event writes, no history, no touch or promote, no token management.
- Retries, backoff, caching, or any session/handshake model. Auth is one optional static bearer token.
- Adapters for Nanite, Tether or any other consumer, and any non-Go client.
- Modelling every server recall filter; only the fields callers use are present.

## Development

```sh
gofmt -l .
GOWORK=off go vet ./...
GOWORK=off go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
