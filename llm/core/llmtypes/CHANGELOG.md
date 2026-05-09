# Changelog

## v0.1.0

Initial alpha release.

- Extracted transport-agnostic LLM data model types from `go-providers`
- Added request, message, tool, stream-event, usage, and capabilities structs
- Added `ChatRequest.EffectiveSystemPrompt()` and `IsTurnComplete()`
