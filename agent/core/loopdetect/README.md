# go-loopdetect

Fingerprint-based detector for repeated tool calls in a per-session sliding window.

It answers one question: in this session, has the same tool been called with the same arguments too many times lately? Each call is hashed (FNV-1a over the tool name and key-order-normalized JSON arguments), the last 10 fingerprints per session are kept, and the third occurrence of one fingerprint in that window returns a `Detection`. The same fingerprint then stays quiet until it ages out of the window.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/go-loopdetect
```

Requires Go 1.26.6 or newer. Standard library only.

## Usage

```go
package main

import (
	"encoding/json"
	"fmt"

	loopdetect "github.com/hollis-labs/go-loopdetect"
)

func main() {
	d := loopdetect.New()
	args := json.RawMessage(`{"path":"main.go"}`)
	for turn := 1; turn <= 3; turn++ {
		det, found := d.Record(loopdetect.Signal{
			SessionID: "session-1",
			TurnID:    fmt.Sprintf("turn-%d", turn),
			ToolName:  "read_file",
			Args:      args,
		})
		fmt.Println(turn, found, det.Reason)
	}
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go). Tune with `WithWindowSize`, `WithThreshold`, `WithMaxSessions` and `WithCallback`.

## Provenance

This library was extracted from the `internal/loopdetect` package of Nanite (source commit `871b49bc2445f3d6417c557a2ac8ab663d92398a` is the last one that touched that directory; the Nanite tree was at `bce9d097a737d19219515caeee8a662aa42d2147`, clean, when lifted). `detector.go` and `detector_test.go` are byte-identical to the Nanite files (checked with `diff`). `types.go` differs only in that its package doc comment, which carried a Nanite ticket reference, was removed; the package documentation now lives in `doc.go`. The 19 Nanite tests were also run in place with `go test -race` (all pass) before the copy. The extraction is ahead of adoption: nothing uses this module yet, including Nanite, which still carries its own copy. New in this repo: `doc.go`, the example, `FuzzNormalizeArgs`, and tests that pin the degenerate-option behavior.

## Compatibility

This module is pre-1.0 and unreleased: any release, including a minor one, may break the exported API, and there is no deprecation period. Pin an exact version and read [CHANGELOG.md](./CHANGELOG.md) before upgrading.

## Known limitations

- Suppression is per session, not per fingerprint. While a fired fingerprint is still in the window, `Record` reports no detection for any other fingerprint in that session. (`TestSuppression_BlocksOtherFingerprintsWhileFiredOneInWindow`)
- Options are not validated. `WithThreshold(0)` fires on the first `Record`; `WithWindowSize(0)` never fires; a negative window size panics on the first `Record` of a new session. Only `WithMaxSessions` ignores non-positive values. See `doc.go`.
- Only top-level JSON keys are sorted. Nested key order, string case and number spelling are significant, so semantically equal but differently spelled arguments get different fingerprints. Non-object input is hashed as raw text.
- Fingerprints are 64-bit FNV-1a: fine against accidental collisions, not against adversarial input.
- The session cap evicts the session created earliest, not the least recently used one. An evicted active session starts over with an empty window.
- The callback runs under the detector's lock; it must be fast and must not call the Detector.
- `Signal.Timestamp` is carried but not used for detection; the window counts calls, not time.
- State is in memory only and is lost on restart.

## Out of scope

- Reflex or rule engines and predicate/action arbitration: this is a fixed-window counter, not a rules system.
- Goal-driven continuation or retry policy for multi-step workflow runs.
- Persistence, transport (HTTP or otherwise) and any UI for showing detections.
- Deciding what to do about a loop: the library reports a `Detection`; the caller acts on it.
- Adoption by any application. No application uses this module yet.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
