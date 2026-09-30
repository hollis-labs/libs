# Changelog

All notable changes to go-chatstream are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- The module: one canonical chat-stream vocabulary, per-dialect decoders, per-target encoders, a reducer and a conformance kit, built on the ratified wire contract (streaming.wire_contract.q27_q28).
- Root package `chatstream`: `Event` (the flat envelope plus per-verb payload), the `Verb`, `PartKind`, `FinishReason` and approval vocabularies, the disjoint-component `Usage` (`Total` is a sum; `UsageFromInclusive`, `UsageFromExclusiveInput`, `Add`, `Sub`), `Capabilities` and its `Validate`, `Reduce`/`Message` (pure; replay from any cursor equals full replay; skips what a gap explains), the `Provider`, `Stream`, `Adapter` and `Decoder` interfaces, and `DecodeFrames`, which keeps exactly one terminal event whatever the upstream does.
- `adapter/anthropic`, `openaichat`, `openairesponses`, `acp`, `claudejson`, `codexjson`: one decoder each, standard library only. Every one synthesizes `run.error` `upstream_truncated` when its upstream ends without a terminal signal (the Anthropic, OpenAI Chat and Responses paths swallowed this silently), and none treats a non-terminal error line or a malformed frame as the end of the run.
- `sink` and `sink/native`, `aisdk`, `agui`, `openaicompat`, `nanitelegacy`: the `Encoder` interface and one encoder per target, framing through go-ssekit, write and flush errors returned.
- `framing` (SSE and line framing into frames), `hubbind` (the go-streamhub seam: publish with the hub's `Seq`, terminal predicate, gaps in-band).
- `conformance`: `Validate`, the reducer oracle, decoder fixtures (recorded frames with inter-chunk timing and golden events) and `CheckTruncation`, plus `fakeserver` (drop, overlap, gap, 404 on resume, stuck), `timing` and `crosscheck`. It ships in v0.1.0.

### Decisions

- One module with subpackages (the go-workflow-host precedent), not three. The hub is go-streamhub's and SSE framing is go-ssekit's: this module composes both and rebuilds neither.
- `Usage` is disjoint components rather than the sketch's including-cache totals (decision D-10), so a consumer cannot double count cache reads.
- No provider SDK dependency: decoders consume already-arrived frames, so `adapter/*` need no build tags.
- `session_takeover` has no generic verb and travels as an application `raw` event.
- Retention across mixed-volume channels on one hub stream is unresolved (CW-20260925-0009) and is not decided here.
