# go-toolresult

Keeps oversized tool results out of an LLM's context without losing them. A
result over a byte budget is stored in full under a host-derived scope and
replaced by a bounded preview plus a pointer; the agent recovers the rest with
a fetch tool (UTF-8-safe byte pages, optionally through an RFC 6901 JSON
pointer) or a search tool (RE2, line based, with continuation).

The design is lifted from Nanite's `internal/tool` result cache, which
supersedes an earlier copy in Loom. Storage sits behind a small `Store`
interface with an in-memory and a SQLite (`database/sql`) implementation.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/go-toolresult
```

Requires Go 1.26.6 or newer. The only non-test dependency is
`github.com/oklog/ulid/v2` (default ids).

## Usage

```go
// Command hello previews an oversized tool result, then reads a field of the
// original back through the pointer.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	toolresult "github.com/hollis-labs/go-toolresult"
	"github.com/hollis-labs/go-toolresult/memstore"
)

func main() {
	ctx := context.Background()
	cache := toolresult.New(memstore.New(), toolresult.Config{})

	// A tool result far bigger than the 2 KiB default budget.
	body := `{"ok":true,"stdout":"` + strings.Repeat("line of output\\n", 400) + `"}`

	// The scope is the host's authenticated session, never a tool argument.
	view, err := cache.Present(ctx, "session-1", toolresult.Meta{Tool: "shell", CallID: "call-1"}, body, 0)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("cached:", view.Cached, "format:", view.Format, "original bytes:", view.OriginalBytes)

	// The model follows the pointer: the first 32 bytes of /stdout.
	page, err := cache.Read(ctx, "session-1", view.CacheID, "/stdout", 0, 32, 0)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("bytes %d..%d of %d, has_more=%t\n", page.Offset, page.End, page.TotalBytes, page.HasMore)
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go).

The pieces:

- `toolresult.Preview`, `Select`, `ReadPage`, `SearchPage`, `CutUTF8` and
  `BudgetForWindow` are pure functions over strings; use them alone if you
  keep your own storage. `CutUTF8` is a rune-safe `s[:n]` for hard caps.
- `Cache` (`New`, `Present`, `Put`, `Read`, `Search`, `Purge`) adds a `Store`,
  a TTL and scope isolation. `HandleFetch` / `HandleSearch` execute the two
  agent tools; `FetchSpec` / `SearchSpec` describe them as plain data so any
  LLM client library can register them.
- `memstore`, `sqlstore` and `storetest` (the conformance suite for other
  stores). `sqlstore.Table` defaults to Nanite's `tool_result_cache` /
  `session_id`; `Table{Name: "wiki_result_cache", ScopeColumn: "caller_id"}`
  matches Loom's existing table, so neither app needs a migration.

### Scope must come from the host

Every entry belongs to a `scope` string, and every read is scoped; an id in
another scope reads as `ErrNotFound`, the same as an absent id. Derive the
scope from an authenticated session or caller. Never read it from tool
arguments: the model controls those, and a self-asserted scope with a shared
default lets one caller read another's results.

### When storing fails

`Present` stores before it previews. If the store errors, it returns the
unmodified body together with the error and never prints a pointer for an id
that was not stored. Fall back to your own truncation in that case.

### Lists are somebody else's job

Item lists, cursors and per-item byte or token fitting belong to go-mcp's
`budget` package. The two compose in application code, never by import: fit
the list with `budget`, `Cache.Put` the full pre-limit JSON, and append the
pointer sentence (`View.Footer` is kept separate from the preview for this) to
the envelope's hint.

## Compatibility

This module is pre-1.0: minor releases may break the exported API. Pin an exact
version, and read [CHANGELOG.md](./CHANGELOG.md) before upgrading — every
breaking change is listed there. The text `HandleFetch`, `HandleSearch`,
`DefaultFooter` and the specs emit is prompt surface. Its text is pinned by 24
goldens captured from copies of Nanite's functions (the harness is not
committed), and a change to it is a breaking change. Parity was checked
against a copy, not Nanite in place; Nanite's adoption should confirm it,
and that its `*sql.DB` passes into `sqlstore` unchanged. `RunPurger` is not
built.

## Out of scope

- List envelopes, item-count limits, opaque cursors, and fitting a slice of
  items to a byte or token budget: go-mcp's `budget` package.
- MCP client hygiene (response size limits, ANSI stripping, injection
  scanning).
- Disk spill of oversized output, and any model catalog: the caller resolves
  the context window and passes it to `BudgetForWindow`.
- Postgres or any non-SQLite dialect; `sqlstore` uses `?` placeholders and RFC
  3339 text times.
- A background purger or scheduler: call `Cache.Purge` when your policy says
  to. Nothing purges automatically, and the default TTL is one hour.
- Logging and metrics; the library has none.
- An LLM client: the tool specs are data with no `go-llm-types` import.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
