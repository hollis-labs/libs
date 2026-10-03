# go-ssekit

A Server-Sent Events writer, serve loop, resumable client and test harness that never look inside the payload.

It is not an event model, a hub or a wire contract: every type carries opaque bytes, an event name and an id string. Do not add event or terminal vocabulary, cursor parsing or arithmetic, replay buffers, CORS or `Connection` headers, or JSON helpers.

## Start Here

- `ssekit` package (module root) — the importable API; `doc.go` is the package documentation. `writer.go` (Writer), `serve.go` (Serve), `source.go` (Source, Merge, PollSource), `resume.go` (ResumeCursor), `read.go` (parser and Read), `client.go` (Client.Stream).
- `conformance` — the exported WHATWG vector table (`Vectors`) and `Run(t, parse)`; `TestRead_WHATWGVectors` runs it against `Read`. Add a vector there, not in `read_test.go`. Its own tests prove `Run` fails on known-bad parsers.
- `ssetest` — the test harness: `script.go` (scripted hostile server), `record.go` (Record/Replay).
- `testdata/capture` — the program that produced the byte-compat expectations; run with `go run ./testdata/capture`.
- `examples/hello/main.go` — the runnable example; the README `## Usage` fence must stay identical to it.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
go test -run '^$' -fuzz FuzzRead -fuzztime 60s .
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either. Standard library only; do not import go-mcp, go-streamhub or tmaxmax/go-sse (`TestRead_WHATWGVectors` is why go-sse lost: it dispatches incomplete events at EOF and hides `retry:`).
- The parser must follow WHATWG: an incomplete event at EOF is discarded (`TestRead_WHATWGVectors`), chunking never changes the result (the "chunking invariant" subtests of `TestRead_WHATWGVectors`, `FuzzRead`), and a CR ends a line immediately without waiting for a following LF (otherwise the last event of a CR-terminated stream is delayed until more bytes arrive).
- `Send` returns every write and flush error and never writes a frame for an invalid id or name (`TestSend_WriteAndFlushErrorsAreReturned`, `TestSend_InvalidFieldRejected`). Control frames carry no `id:` so they do not clobber `Last-Event-ID` (`TestSend_ByteCompatWithApplicationFraming`).
- `NewWriter` clears both the write and the read deadline; clearing only the write deadline still lets a server `ReadTimeout` kill a long GET (`TestNewWriter_SurvivesServerTimeouts`, which includes a control that must be cut).
- The client's idle watchdog is driven by any bytes, comments included, and is paused while the consumer runs (`TestStream_CommentsKeepTheWatchdogQuiet`, `TestStream_SlowConsumerIsNotIdle`). A `retry:` from the server is honored (`TestStream_ServerRetryIsHonoured`).
- The client never de-duplicates or compares ids on its own; only `WithContinuity` does, and only when the id changes.
- Tests use `httptest` and `testing/synctest`; no fixed ports, no sleeps that decide a result. Goroutine leaks are caught by `synctest.Test` (bubbles must drain) and by `noLeaks` in `helpers_test.go`. Keep new concurrent tests `-race -count=20` clean.
- Behavioral equivalence with the applications this was lifted from is NOT established (see README Provenance). Do not describe the library as identical to them.
