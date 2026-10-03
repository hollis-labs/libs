# Examples

Runnable demonstrations of the public API. Each subdirectory is its own
`package main` and can be executed with `go run ./examples/<name>` from
the module root.

| Directory     | Demonstrates                                                         |
| ------------- | -------------------------------------------------------------------- |
| `chatrequest` | Building a `ChatRequest` with a system prompt, message, and tool.    |
| `streamevent` | Iterating `StreamEvent` values and using `IsTurnComplete`.           |
| `slots`       | Composing a system prompt from `SlotBlock` regions.                  |

These examples make no network calls and require no API keys.
