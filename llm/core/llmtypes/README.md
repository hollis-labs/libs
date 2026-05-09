# go-llm-types

Transport-agnostic data structures for LLM chat/completion workflows in the
Hollis Labs portfolio.

## Status

Alpha (`v0.1.x`). Introduced during SP-20260508-0001 to separate shared data
model types from provider implementations and optional contracts.

## What's Here

- Request and conversation types: `ChatRequest`, `ChatMessage`, `SlotBlock`
- Tool and content types: `ToolDefinition`, `ToolUseBlock`, `ContentBlock`
- Streaming/event types: `StreamEvent`, `EventType`, `ThinkingBlock`
- Metadata types: `Usage`, `CompleteResult`, `ProviderCapabilities`
- Helpers: `IsTurnComplete`, `ChatRequest.EffectiveSystemPrompt`

## What's Not Here

`go-llm-types` intentionally contains no provider interfaces, no transport
implementations, and no rate-budget or retry helpers. Those belong in
`go-llm-contracts` and the implementation repos.

## Companion Modules

- `github.com/hollis-labs/go-llm-contracts` — provider interfaces and shared
  rate-budget primitives
- `github.com/hollis-labs/go-providers` — PTY/CLI/subprocess provider adapters
