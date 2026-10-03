# go-chatstream

One canonical chat-stream event vocabulary with per-dialect decoders, per-target encoders, a reducer and a conformance kit.

It is not: a hub, an SSE library, an HTTP client for any provider, a request builder, a usage ledger or a place for application event types. The hub is go-streamhub (its own boundary: "do not add an event model"), SSE framing is go-ssekit (its own boundary: "do not add event or terminal vocabulary"); this module is the event model and composes both. A competent-looking change that counts its own cursor, writes SSE bytes by hand, or makes an HTTP call is the mistake this repo attracts.

## Start Here

- `event.go` (verbs, `Event`, the payload fields per verb), `usage.go`, `finish.go`, `capabilities.go`, `meta.go` (the Meta conventions), `provider.go` (`Decoder`, `Adapter`), `decode.go` (`DecodeFrames`), `reduce.go` (`Reduce`/`Message`).
- `internal/decodekit` is the lifecycle bookkeeping every decoder embeds; `internal/anthropicwire` is the Anthropic block state machine shared by `adapter/anthropic` and `adapter/claudejson`.
- `adapter/<dialect>` — one decoder each, with `testdata/*.frames.json` (recorded frames) and `*.golden.json` (the events they must produce).
- `sink/` — the `Encoder` interface; `sink/sinktest` is the shared scenario kit every encoder runs.
- `conformance/` — `Validate`, the reducer oracle, `CheckDecoder`/`CheckTruncation`; `crosscheck` runs every dialect through every sink.
- `hubbind/` — the go-streamhub seam. `framing/` — SSE and line framing into `Frame`s.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
CHATSTREAM_UPDATE_GOLDEN=1 go test ./adapter/<dialect>/   # rewrite goldens, then READ the diff
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either. Dependencies are go-ssekit and go-streamhub only; no provider SDK (decoders parse JSON with the standard library).
- **Exactly one terminal event, and truncation is never success.** Every decoder closes what is open and emits `run.error` `upstream_truncated` when its upstream ends without a terminal signal (`conformance.CheckTruncation` runs at every frame boundary of every fixture; `TestDecodeFramesTruncationIsAnErrorEvent`, `TestBaseNeverEmitsPastTheTerminalEventAndCloseIsIdempotent`). A decoder never emits past its terminal event (`decodekit.Base.Emit` refuses).
- **Usage is disjoint components.** Never add "input plus cache" anywhere: `TestUsageTotalNeverDoubleCounts` and each adapter's usage test guard it. Convert in the decoder (`UsageFromInclusive` for OpenAI-shaped counts that include their subsets, `UsageFromExclusiveInput` for Anthropic-shaped).
- **An error is terminal only when the dialect says so.** A CLI error line, a Codex item error or a malformed frame is not the end of the run (the spike's mapper got this wrong): each adapter's `TestOnlyResultIsTerminal`-style test and the malformed-frame fixtures.
- **A frame that cannot be decoded is preserved, not dropped or fatal**: a `raw` event, whose payload is a JSON string when the bytes are not JSON (`TestRawEventSurvivesMarshalWhateverTheBytes`).
- **Meta conventions are enforced, not advisory.** `conformance.Validate` fails a `tool_call` part with no Meta `name` and a `tool_result` part whose `call_id` is not an earlier tool_call part id: the encoders render from them (`crosscheck` proves every dialect reaches every sink under its tool name).
- **`Event.Seq` is the hub's.** Decoders leave it zero; `hubbind.Publish` sets it from the record. Resume is header-only (`TestResumeCursorInTheQueryStringIsIgnored`): never pass `ssekit.WithQueryKeys`.
- **Goldens are reviewed, not trusted.** Regenerating a golden and committing it unread defeats the fixture: read every changed line against the dialect's spec.
- `Reduce` is pure and replay from any cursor equals full replay (`conformance.CheckReplayEquivalence`); after a gap it skips what the gap explains instead of failing (`TestReduceAfterAGapSkipsWhatItsOpeningLost`).
