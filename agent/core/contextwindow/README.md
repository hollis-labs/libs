# go-context-window

Fixed, ordered context-slot system with per-slot budgets, cache-key tracking and escalating compaction.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/substrate/agent/contextwindow
```

## Usage

```go
package main

import (
	"fmt"

	contextwindow "github.com/hollis-labs/substrate/agent/contextwindow"
)

func main() {
	cw := contextwindow.NewContextWindow(200_000, contextwindow.DefaultEstimator{})
	cw.SetContent(contextwindow.SlotSystem, "You are a careful assistant.")
	cw.SetContent(contextwindow.SlotConversation, "user: hello")

	// Blocks come back in contextwindow.SlotOrder; empty slots are skipped.
	for _, b := range cw.Assemble() {
		fmt.Printf("%s changed=%v\n", b.SlotName, b.Changed)
	}
}
```

`Assemble` also records each slot's SHA-256 cache key, so the next call reports
which slots changed. Call it once per turn. When the conversation slot goes
over budget, `CompactionPipeline.Run` escalates through four stages in order:
drop enrichment, dedupe tool results, summarize the oldest messages, strip tool
blocks. See the package examples for a runnable compaction run.

The package holds the slot table and the mechanism only. What goes into each
slot is up to the consumer.

**The order of `SlotOrder` is cache-load-bearing.** Reordering, inserting or
omitting a slot changes the cacheable prefix consumers build from the
assembled blocks. See [INVARIANTS.md](./INVARIANTS.md).

## Compatibility

This module is pre-1.0 and unreleased: minor releases may break the exported
API. Pin an exact version, and read [CHANGELOG.md](./CHANGELOG.md) before
upgrading. Every breaking change is listed there. Any change to `SlotOrder`,
to a slot name, or to the default budgets or compactability is breaking for
every consumer's prompt cache and will be called out as such.

## Out of scope

- Enforcing the seven Nanite dispatch-flavor invariants. Nanite's own
  `slot_invariants_test.go` does that; this module has no dispatcher.
  [INVARIANTS.md](./INVARIANTS.md) states only the mechanism-level guarantees.
- Provider cache-marker placement (for example Anthropic `cache_control`).
  This module supplies ordered slots and their budget and compactability
  data; deciding where markers go is the consumer's or an adapter's job.
- Rendering slot content: permission summaries, workspace instruction walk-up,
  skill listings, universal rules. This module holds the empty slot, its
  budget and its position.
- Any transport, provider client or storage. `Summarizer` and
  `CompactionEventWriter` are interfaces the consumer implements;
  `ProviderSummarizer` adapts an `llm-core/llmcontracts` provider.

## Development

From the repository root, run `scripts/check agent`. Read
[AGENTS.md](./AGENTS.md) before changing `slot.go`.

## License

MIT — see the [repository license](../../LICENSE).
