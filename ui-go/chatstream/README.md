# go-chatstream

One canonical chat-stream event vocabulary with per-dialect decoders, per-target encoders, a reducer and a conformance kit.

Providers and protocols stream differently: Anthropic Messages and OpenAI Chat and Responses over SSE, ACP over JSON-RPC lines, the Claude and Codex CLIs over JSON lines, AG-UI and the AI SDK's UI stream toward browsers. This module standardizes the seams, not a dialect: a `Decoder` per dialect produces `Event`s, an `Encoder` per target consumes them, `Reduce` folds them into a `Message`, and the conformance kit ships with all of it.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

Pre-1.0. The vocabulary follows the ratified wire contract (the event envelope, an AG-UI-style encoder, header-only `Last-Event-ID` resume, gaps signalled in-band) and is additive: new verbs, part kinds and optional fields do not change `SchemaVersion`.

## Install

```sh
go get github.com/hollis-labs/go-chatstream
```

## Usage

```go
package main

import (
	"context"
	"fmt"
	"strings"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/adapter/anthropic"
	"github.com/hollis-labs/go-chatstream/framing"
)

// A recorded Anthropic Messages stream that stops before message_stop: the
// connection dropped mid-answer.
const stream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude","usage":{"input_tokens":25,"cache_read_input_tokens":100,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello, wor"}}

`

func main() {
	dec := anthropic.New().NewDecoder(chatstream.DecodeOptions{})
	var events []chatstream.Event
	for ev, err := range chatstream.DecodeFrames(context.Background(), dec, framing.SSE(strings.NewReader(stream))) {
		if err != nil {
			panic(err)
		}
		events = append(events, ev)
	}

	// Truncation is never success: the stream ends with exactly one run.error.
	last := events[len(events)-1]
	fmt.Println(last.Verb, last.Code, last.Retryable)

	msg, err := chatstream.Reduce(events, nil)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s %q, %d tokens\n", msg.Status, msg.Text(), msg.Usage.Total())
}
```

The same program lives in [`examples/decode`](./examples/decode/main.go).

## Packages

| Package | What it is |
|---|---|
| `chatstream` | `Event`, the `Verb` and `PartKind` vocabularies, `FinishReason`, the disjoint-component `Usage`, `Capabilities`, `Reduce`/`Message`, the `Provider`/`Adapter`/`Decoder` interfaces and `DecodeFrames` |
| `chatstream/adapter/anthropic`, `openaichat`, `openairesponses`, `acp`, `claudejson`, `codexjson` | one `Decoder` per dialect, each separately importable; stdlib only, no provider SDK |
| `chatstream/sink` and `sink/native`, `aisdk`, `agui`, `openaicompat`, `nanitelegacy` | the `Encoder` interface and one encoder per target |
| `chatstream/framing` | SSE (through go-ssekit's parser) and line framing into `Frame`s |
| `chatstream/hubbind` | the documented seam to go-streamhub: publish with the hub's `Seq`, terminal predicate, events from a subscription with gaps in-band |
| `chatstream/conformance` | `Validate`, the reducer oracle, decoder fixtures and the truncation check; `conformance/fakeserver` (drop, overlap, gap, 404, stuck), `conformance/timing`, `conformance/crosscheck` (every dialect through every sink) |

## The guarantees

- **One terminal event.** Exactly one `run.finish`, `run.error` or `run.abort` ends every run, and nothing follows it.
- **Truncation is never success.** When the upstream ends without its terminal signal, the decoder closes what is open and emits `run.error` with code `upstream_truncated`. `DecodeFrames` enforces it for a whole stream, and `conformance.CheckTruncation` proves it at every frame boundary of a recorded stream.
- **Usage cannot be double counted.** `Usage` is disjoint components (uncached input, cache read, cache write, output, reasoning) and `Total` is their sum. Providers whose prompt count includes cache reads (OpenAI) and providers whose input count excludes them (Anthropic) convert once, in the decoder, with `UsageFromInclusive` and `UsageFromExclusiveInput`.
- **Capabilities are declared, not faked.** Each adapter's `Capabilities` states what its dialect streams (token, chunk or whole), and `Validate` rejects a contradictory declaration.
- **The hub owns the cursor.** `Event.Seq` is the go-streamhub record's `Seq`; this module never counts its own, and resume is by the `Last-Event-ID` header only. Resume replays events, not encoder state: the stateful encoders (`aisdk`, `agui`, `openaicompat`, `nanitelegacy`) must be fed the run from `run.start` (a fresh encoder given only a tail returns `sink.ErrOutOfOrder` rather than silently dropping content), so a resumed subscription replays the run from its first event into a fresh encoder and writes only the frames past the client's cursor.

## Compatibility

This module is pre-1.0: minor releases may break the exported API. Pin an exact
version, and read [CHANGELOG.md](./CHANGELOG.md) before upgrading — every
breaking change is listed there.

## Out of scope

- A hub, cursors, replay or slow-consumer policy: that is [go-streamhub](https://github.com/hollis-labs/go-streamhub). SSE framing and the resumable client: [go-ssekit](https://github.com/hollis-labs/go-ssekit).
- Making HTTP calls to any provider. Decoders consume frames that already arrived; where HTTP LLM providers live is a separate decision.
- Request modelling (prompts, tools, sampling), usage ledgers or persistence, and provider SDK wrappers.
- Application event types (Nanite's `plugin_envelope`, `panel_signal`, ...): they ride as `raw` events with their own dialect name.
- A durable raw event log: a debug-flag-gated, write-time-redacted feature of the application, not a default here.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate. Golden files under `testdata/` are rewritten with `CHATSTREAM_UPDATE_GOLDEN=1`; review the diff before committing it.

## License

MIT — see [LICENSE](./LICENSE).
