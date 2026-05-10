# Changelog

## v0.2.0 — 2026-05-10

Public-release prep. No public-API changes; additions only.

- Added `examples/` directory with three runnable demos:
  - `examples/chatrequest` — building a `ChatRequest` with system prompt, message, and tool
  - `examples/streamevent` — iterating `StreamEvent` values and using `IsTurnComplete`
  - `examples/slots` — composing a system prompt from `SlotBlock` regions
- Rewrote `README.md` for a public audience: install snippet, runnable
  quickstart, status banner, godoc link, license line.
- Extended `.gitignore` to keep agent-tooling scratch files out of the
  public tree.

## v0.1.0

Initial alpha release.

- Extracted transport-agnostic LLM data model types from `go-providers`
- Added request, message, tool, stream-event, usage, and capabilities structs
- Added `ChatRequest.EffectiveSystemPrompt()` and `IsTurnComplete()`
