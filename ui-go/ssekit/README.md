# go-ssekit

A Server-Sent Events writer, serve loop, resumable client and test harness that never look inside the payload.

Seventeen hand-written SSE writers in six applications disagree on keepalive, buffering headers, write deadlines, whether a failed flush is noticed, and which of four rules picks the resume cursor. Their ten hand-written line parsers cap events silently, flush half an event at EOF, and none of the Go clients reconnects with a cursor. This library is the one place that gets the transport right: every type carries opaque bytes, an event name and an id string, and nothing else.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/go-ssekit
```

Requires Go 1.26.6 or newer. Standard library only; no third-party modules.

## Usage

```go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"

	ssekit "github.com/hollis-labs/go-ssekit"
)

func main() {
	// Server: an event stream over a channel. Serve writes each event, sends a
	// keepalive comment when quiet, and stops after the terminal event.
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w, err := ssekit.NewWriter(rw)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		// Resume: what the client already has. What the id means is up to you.
		from, _ := ssekit.ResumeCursor(r, ssekit.WithQueryKeys("from"))
		start, _ := strconv.Atoi(from)

		events := make(chan ssekit.Event, 8)
		for i := start + 1; i <= 3; i++ {
			events <- ssekit.Event{ID: strconv.Itoa(i), Name: "tick", Data: []byte("tick " + strconv.Itoa(i))}
		}
		events <- ssekit.Event{Name: "done", Data: []byte("bye")}

		_ = ssekit.Serve(r.Context(), w, ssekit.ChanSource(events),
			ssekit.WithTerminal(func(e ssekit.Event) bool { return e.Name == "done" }))
	}))
	defer srv.Close()

	// Client: Stream reconnects with the last event id if the connection drops.
	client := ssekit.NewClient(srv.Client())
	newReq := func(lastEventID string) (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL+"?from="+lastEventID, nil)
	}
	for ev, err := range client.Stream(context.Background(), newReq, ssekit.WithIsTerminal(func(e ssekit.Event) bool { return e.Name == "done" })) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("id=%q event=%s data=%s\n", ev.ID, ev.Name, ev.Data)
	}
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go); run it with `go run ./examples/hello`.

## What it does

| Piece | What it gives you |
|---|---|
| `NewWriter`, `Writer.Send`, `Writer.Comment` | Headers (`Content-Type`, `Cache-Control: no-cache, no-transform`, `X-Accel-Buffering: no`), status 200, write and read deadlines cleared (best effort), one flush per event, and every write or flush error returned. `ErrNoFlusher` before any header is touched; CR or LF in an id or event name is `ErrInvalidField` (frame injection). |
| `Serve`, `Source` | Copies a `Source` to a `Writer` with a keepalive comment after 15 s of quiet, an optional maximum lifetime, a terminal-event predicate, and context cancellation. Returns write errors so the caller can abort upstream work. |
| `ResumeCursor` | Reads `Last-Event-ID` and/or named query parameters into an opaque string. `HeaderFirst` (default), `QueryFirst`, or `Newest(cmp)` with your comparison. |
| `Read` | A WHATWG event-stream parser over any `io.Reader`, as an `iter.Seq2[Event, error]`. |
| `conformance.Run`, `conformance.Vectors` | The WHATWG raw-frame vector table `Read` is tested against, exported so any other SSE parser can run it (whole, one byte per `Read`, split at every offset). Raw-frame parsing only: no reconnection or HTTP behavior. |
| `Client.Stream` | Connects, parses, and reconnects with `Last-Event-ID` (and hands it to your request builder for apps that resume by query parameter). Backoff 1, 2, 4, 8, 16 s with 20% jitter, honors a server `retry:`, idle watchdog (45 s) driven by any bytes including comments, terminal predicate, final-status policy (4xx except 408 and 429), continuity check yielding `*GapError`, reconnect limit. |
| `ChanSource`, `SourceFunc`, `Merge`, `PollSource` | Sources: a channel, a function, a live fan-in of several sources, and a poll loop with an opaque cursor. |
| `ssetest.Script` | A scripted, hostile server: `Emit`, `Drop`, `Close`, `Overlap`, `Gap`, `Status`, `Stall`, `Burst`, `Retry`, `Comment`, `Raw` chunks, and a record of every request seen. |
| `ssetest.Record`, `ssetest.Replay` | A stream as frames with inter-chunk timing, and playback at the recorded offsets. Deterministic under `testing/synctest`. |

`Event.ID` is `""` when a control frame should not move the client's stored `Last-Event-ID`; the writer then emits no `id:` line. On the wire the field order is `id`, `event`, `retry`, `data`.

### The wire bytes match the framing the applications write

For the same input, `Send` produces the same bytes as the `fmt.Fprintf` framing statements in Nanite (`host_runtime_feed.go`, `messages.go`), Tangent (`turns.go`) and Tether (`events.go`). That claim is limited: `wire_test.go`'s expectations came from running `testdata/capture`, a small program holding **copies of those statements** with the payloads stubbed. The applications themselves were not run. See Provenance.

### The parser is not go-sse

The brief allowed wrapping `github.com/tmaxmax/go-sse` v0.11.0 if a first spike passed the WHATWG vectors. It did not, so `Read` is a stdlib parser (about 150 lines in `read.go`). Run against the vectors, `sse.Read` (default config):

- returned an event still incomplete at EOF (`data: a\n\ndata: b\n` yielded `b`; the specification says discard it);
- returned an error, and no event, for `data: b` with no final newline;
- dispatched a block with a lone `id:` and no `data:` line (an event with empty data);
- does not expose `retry:` at all, which the client needs;
- reports an oversize event as a bare `bufio.Scanner: token too long`.

CRLF, lone CR, chunking one byte at a time and BOM handling passed. The spike was a throwaway program run once against those inputs; the vectors it used live on as `conformance.Vectors`, run by `TestRead_WHATWGVectors`, which now runs against the stdlib parser.

## Defaults the brief left open

These follow the brief's stated defaults and are all overridable: header wins over query in `ResumeCursor`; `Cache-Control: no-cache, no-transform` and `X-Accel-Buffering: no` and nothing else (no CORS, no `Connection`); heartbeat 15 s and client idle timeout 45 s; `Merge` and `PollSource` are included.

## Known limitations

- **Cursors are yours.** There is no replay buffer and no cursor arithmetic. A reconnecting client that sends `Last-Event-ID` gets whatever your handler does with it. A gap or an overlap is detected only if you pass `WithContinuity`, and overlapping events are delivered, not dropped: de-duplicate in the application.
- **Serve reads one event ahead.** An event already taken from the `Source` but not yet written when a write fails or the context ends is lost from this connection. Resume by cursor is what recovers it.
- **`Source.Next` must return when its context is canceled.** `Serve` waits for the goroutine that calls it; a source that ignores cancellation blocks `Serve` from returning. `Merge` starts goroutines on its first `Next` and binds them to that call's context.
- **The heartbeat measures quiet, not wall-clock.** It fires after the configured interval without a write, and every event resets it. Existing applications use a fixed ticker; the effect on proxies is the same.
- **Deadline clearing is best effort.** A `ResponseWriter` wrapper that does not implement `SetWriteDeadline` or `SetReadDeadline` keeps the server's timeouts, silently. Use `WithMaxLifetime` below the timeout when you cannot unwrap.
- **HTTP/1.1 only in tests.** The suites use `httptest` over HTTP/1.1. HTTP/2, real proxies and Windows were not exercised.
- **Client idle watchdog pauses while your loop body runs.** Consumer slowness is not read as server silence, so a consumer that blocks forever also never trips it.
- **A server `retry:` replaces the backoff schedule** for the rest of that stream (the specification's reconnection time), without jitter. It is capped at 5 minutes by default; `WithMaxServerRetry` (a `ClientOption`) changes the cap.
- **A stalled client connection can block `Serve` inside a write.** `NewWriter` clears the deadlines, so a write to a peer whose TCP connection has silently stalled has no timeout, and neither context cancellation nor `WithMaxLifetime` can preempt it. Enable TCP keepalive on the listener, or set a per-write deadline with `http.ResponseController.SetWriteDeadline` before each `Send`. The library does not do this for you.
- **A 200 that is not `text/event-stream` is final** (`ErrNotEventStream`), as is a 204 (a clean end).
- **Payloads are not validated as UTF-8.** The specification decodes as UTF-8 with replacement; this library passes bytes through. A lone CR inside `Data` is written as a line break, so it reads back as `\n`.
- **`WithMaxEventBytes` counts the raw block** (field names, values, line breaks, comments), default 1 MiB, and the client's equivalent is `WithStreamMaxEventBytes`.
- **Jitter uses `math/rand/v2`** and cannot be seeded from outside; use `WithBackoff(schedule, 0)` for exact delays.

## Provenance

What the code was lifted from, and how it was checked.

- **Transcribed from reading, not run:** the writer's headers, deadline clearing, keepalive and id-less control-frame semantics (Nanite `host_runtime_feed.go`, `sse.go`, `messages.go`), the poll, keepalive and lifetime loop (Tangent `turns.go`, `server.go`), the frame construction (Tether `events.go`), the backoff values (`go-mcp/supervise/policy.go`, copied as numbers only) and the chunk-oriented reader idea (`go-tether-client/events.go`, deliberately not copying its EOF flush or `ParseInt`). None of the applications was built or run.
- **Captured from running a copy:** the byte expectations in `TestSend_ByteCompatWithApplicationFraming` came from `go run ./testdata/capture`, which holds copies of the applications' `fmt.Fprintf` framing statements with the payloads stubbed. That establishes the framing, not the applications' behavior.
- **Not read:** Tangent `hitl.go`, `docs.go` and `channels.go` (assumed identical to `turns.go` in shape), Tether `internal/client/*` and `federation/peerstore.go`, and the application test files the brief lists as scenario sources. Nothing here depends on them.
- **Not imported:** `go-mcp/compat` has an SSE client sanitizing reader for one SDK transport (it hands out one event block per `Read`). That is a different job and stays there; this library neither imports nor duplicates it.

## Compatibility

This module is pre-1.0 and unreleased. There are no external consumers, so exported names change without deprecation shims. When a first tag exists, minor releases before v1 may break the exported API; pin an exact version and read [CHANGELOG.md](./CHANGELOG.md) before upgrading. Nothing is promised about the wire bytes beyond what the WHATWG specification defines.

## Out of scope

- Event vocabulary, terminal vocabulary, dialect encoders (native, AI SDK, AG-UI, OpenAI-compatible, A2A), reducers, usage arithmetic, telemetry.
- Cursor parsing or arithmetic, replay buffers and fan-out hubs (that is a separate library), persistence.
- CORS and `Connection` headers, authentication, mTLS, routing, JSON helpers.
- A TypeScript or browser client.
- Making the bytes of any particular application's stream identical beyond the framing described above.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
go test -run '^$' -fuzz FuzzRead -fuzztime 60s .
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT, see [LICENSE](./LICENSE).
