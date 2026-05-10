# go-llm-types

Transport-agnostic data structures for LLM chat/completion workflows. This
module defines the shared request, response, tool, and stream-event types
used across the Hollis Labs LLM toolchain. It deliberately holds **data
types only** — no transport, no provider interfaces, no retry logic.

## Status

Pre-1.0 (`v0.x`). The API may evolve; breaking changes will be called out in
[CHANGELOG.md](./CHANGELOG.md) and a minor-version bump.

## Install

```sh
go get github.com/hollis-labs/go-llm-types
```

## Quickstart

```go
package main

import (
	"fmt"

	llmtypes "github.com/hollis-labs/go-llm-types"
)

func main() {
	req := llmtypes.ChatRequest{
		Model:        "claude-3-5-sonnet",
		SystemPrompt: "You are a helpful assistant.",
		Messages: []llmtypes.ChatMessage{
			{Role: "user", Content: "Hello!"},
		},
		MaxTokens: 1024,
	}

	fmt.Println(req.EffectiveSystemPrompt())
}
```

See [`examples/`](./examples) for runnable demos covering `ChatRequest`,
`StreamEvent`, and slot composition.

## Documentation

Full API reference: [pkg.go.dev/github.com/hollis-labs/go-llm-types](https://pkg.go.dev/github.com/hollis-labs/go-llm-types).

## What's Here

- Request and conversation types: `ChatRequest`, `ChatMessage`, `SlotBlock`
- Tool and content types: `ToolDefinition`, `ToolUseBlock`, `ContentBlock`
- Streaming / event types: `StreamEvent`, `EventType`, `ThinkingBlock`
- Metadata types: `Usage`, `CompleteResult`, `ProviderCapabilities`
- Helpers: `IsTurnComplete`, `ChatRequest.EffectiveSystemPrompt`

## What's Not Here

`go-llm-types` intentionally contains no provider interfaces, no transport
implementations, and no rate-budget or retry helpers. Those belong in
companion modules and provider-specific adapters.

## Companion Modules

- `github.com/hollis-labs/go-llm-contracts` — provider interfaces and shared
  rate-budget primitives
- `github.com/hollis-labs/go-providers` — PTY / CLI / subprocess provider
  adapters

## License

MIT — see [LICENSE](./LICENSE).
