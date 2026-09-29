# go-toolselect

A deterministic BM25 ranker for MCP tool catalogs, and a pure evaluator that
decides which tools of which servers a caller sees.

## Packages

| Package | Purpose | Imports |
|---|---|---|
| `toolselect` | Ranks a catalog against a free-text query: an exact name always outranks a partial match, ties break by name, the same inputs always give the same order. Also applies the shared include / exclude / order rule schema. | standard library only |
| `toolselect/profile` | Evaluates a profile (servers on or off, allow and deny globs, read-only, pinned order) against a catalog and reports the visible tools and why each hidden tool is hidden. | standard library only |
| `toolselect/launch` | Derives a per-launch profile from an Assignment's `grants.mcp` ceiling and a base profile. | `profile`, `agent-contracts-leaf` |

It decides what a caller *sees*, not what it may *call*. Authorization belongs
to go-permission.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/go-toolselect
```

`toolselect/launch` depends on `github.com/hollis-labs/agent-contracts-leaf`
(v0.1.0), a private module: set `GOPRIVATE=github.com/hollis-labs`. Importing
only `toolselect` or `toolselect/profile` pulls in nothing outside the standard
library.

## Usage

```go
// Command hello ranks a small tool catalog and evaluates a visibility profile.
package main

import (
	"fmt"
	"log"

	toolselect "github.com/hollis-labs/go-toolselect"
	"github.com/hollis-labs/go-toolselect/profile"
)

func main() {
	catalog := toolselect.Catalog{Tools: []toolselect.Tool{
		{Server: "torque", Name: "torque_task_create", Description: "Create a task in the tracker"},
		{Server: "torque", Name: "torque_task_list", Description: "List tasks"},
		{Server: "files", Name: "files_read", Description: "Read a file from disk"},
	}}
	hits, err := toolselect.Rank(catalog, "create task", nil, toolselect.WithMaxResults(2))
	if err != nil {
		log.Fatal(err)
	}
	for _, h := range hits {
		fmt.Println(h.Tier, h.Tool.Name)
	}

	yes := true
	visible, hidden, err := profile.Evaluate(
		profile.Catalog{Servers: []profile.Server{
			{ID: "torque", Tools: []profile.Tool{
				{Name: "torque_task_list", ReadOnly: &yes},
				{Name: "torque_task_create"}, // hint undeclared: nil stays nil
			}},
			{ID: "files", Tools: []profile.Tool{{Name: "files_read", ReadOnly: &yes}}},
		}},
		profile.Profile{Servers: map[string]bool{"files": false}, ReadOnly: true},
	)
	if err != nil {
		log.Fatal(err)
	}
	for _, v := range visible {
		fmt.Println("visible:", v.Name)
	}
	for _, h := range hidden {
		fmt.Println("hidden:", h.Name, h.Reason)
	}
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go).

## How the launch derivation works

`launch.FromAssignment(base, assignment)` narrows a base `profile.Profile` by
the Assignment's `grants.mcp` `{allow, deny}`. Grants are a ceiling: the result
shows no tool the base hides. Grant entries are `path.Match` globs on the
origin-qualified tool name, like `ToolsAllow` and `ToolsDeny`; to scope a grant
to a server, use a name glob such as `github_*`.

Deny entries are unioned. A grant allow list is intersected with the base's
(or used as is when the base allows everything); globs are never intersected
symbolically, so an entry survives only when one side is a literal name the
other matches, or the two are identical. Anything else is dropped, which only
narrows. An empty result allows no tool, never an empty list (which would
allow all). Every other field is copied, and the base is not modified.

Known limitation: dropping overlapping glob-versus-glob entries is deliberately
conservative. It can only over-restrict, never over-grant, so some legitimate
overlapping combinations (for example base `a_*` and grant `a_b*`) narrow more
than strictly necessary. Real glob intersection is not implemented. Grants also
name tools only, not servers; per-server enable/disable stays in
`profile.Profile.Servers`.

```go
derived, err := launch.FromAssignment(base, assignment)
```

## How ranking works

Each tool is one document: name, title, description and tags, tokenized on
every non-letter, non-digit rune (so `torque_task_create` is three words),
lower-cased, with tokens under two characters and a 46-word stopword list
dropped. Document frequencies are catalog-wide, so a score does not change with
the rules in play.

Output order is by tier first, then score:

| Tier | Meaning |
|---|---|
| `TierPinned` | pinned by a matching `ActionOrder` rule, in `Pin` order |
| `TierExactName` | query equals `Name` or `Title`, ignoring case |
| `TierPrefix` | lower-cased `Name` is a prefix of the query, or the reverse |
| `TierBM25` | everything else, by BM25 score descending |

Ties break by `Tool.Name` ascending, so the order is total. A tool that shares
no term with the query and matches no tier is not returned; an empty query
returns only pinned tools. `WithK1B`, `WithMaxResults` and `WithStopwords`
tune the call (defaults k1 1.2, b 0.75, unlimited).

Rules follow go-toolbroker's algorithm: applicable rules (by `Intent` glob on
the query) run in priority order, equal priorities in declaration order;
exclude always wins; once an include rule applies only included tools survive.

## How the profile evaluator works

`profile.Evaluate(catalog, profile)` checks each tool against, in order: a
disabled server, `ToolsDeny`, `ReadOnly`, `ToolsAllow`. The first that hides it
names the cause. Visible tools are ordered by `Profile.Order`, then server
position in the catalog, then name. The zero `Profile{}` shows everything.

A server id in `Profile.Servers` that the catalog lacks is `ErrUnknownServer`.
A malformed allow or deny glob is `ErrBadPattern`. `Order` and `AlwaysLoad`
are exact tool names.

MCP annotation hints (`ReadOnly`, `Destructive`) are `*bool`: nil means the
upstream did not declare the hint, and it is passed through as nil, never
turned into false. With `Profile.ReadOnly` set, a nil hint is hidden.

## Compatibility

This module is pre-1.0 and unreleased: minor releases may break the exported
API, and there is no compatibility promise yet. Pin an exact version, and read
[CHANGELOG.md](./CHANGELOG.md) before upgrading; every breaking change is
listed there.

## Out of scope

- Authorization or call-time enforcement. Visible does not mean callable;
  `ReadOnly` is advisory and annotations are untrusted.
- Loading profiles or rules from YAML or JSON. Hosts build `Profile` and `Rule`
  values directly.
- Token-budget estimation or pruning.
- Tool naming, prefixing or collision policy; tools arrive already qualified.
- Tool enrichment, override blocks or named-intent detection.
- Per-field weighting (BM25F). One concatenated document per tool.
- A status tool that reports `HiddenReason`, and progressive-discovery protocol
  extensions.
- Deriving a profile from anything but an `agent-contracts-leaf` Assignment,
  and server-level grants that need a catalog to resolve.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT, see [LICENSE](./LICENSE).
