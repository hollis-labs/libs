# go-loopdetect

Fingerprint-based detector for repeated tool calls in a per-session sliding window.

It is not a reflex engine, a retry or continuation policy, or a persistence layer: it counts repeated (tool, args) fingerprints in memory and reports a `Detection`. Do not add storage, transport, rule vocabularies or dependencies.

## Start Here

- `loopdetect` package (module root) — the importable API; `doc.go` is the package documentation. `detector.go` (Detector, options, fingerprinting), `types.go` (constants and value types).
- `detector_test.go` — the tests carried over from Nanite, byte-identical to the source. Do not edit them to make a change pass.
- `sharp_edges_test.go` — pins degenerate-option and suppression behavior.
- `examples/hello/main.go` — the runnable example; the README `## Usage` fence must stay identical to it.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Standard library only, no hollis libs: `go list -deps ./...` shows nothing outside the stdlib and this module.
- `Detector.windows` and `Detector.sessionFIFO` move in lockstep: `getOrCreate` eviction and `Reset` change both. Guards: `TestDetector_EvictsOldestSessionAtConfiguredCap`, `TestDetector_ResetRemovesSessionFromFIFO`.
- `Reset` on an unknown session is a no-op, not an error or panic (`TestReset_ClearsWindow`, `TestDetector_ResetRemovesSessionFromFIFO`).
- Key order in top-level arguments must not change the fingerprint (`TestArgsNormalization_KeyOrderIndependent`); differing values must (`TestArgsNormalization_DifferentValues_NoFalsePositive`). Never lowercase or otherwise fold string values.
- A fired fingerprint suppresses further detection until it leaves the window (`TestSuppressionAfterDetection`, `TestDetectionRefires_AfterFingerprintExitsWindow`); sessions never share state (`TestSessionIsolation`).
- The detector is safe for concurrent use (`TestConcurrentRecord_RaceSafe`); run with `-race`. The callback runs under the lock, so never call the Detector from it.
- Options are intentionally unvalidated in v1; the degenerate behaviors are pinned, not fixed (`TestWithThreshold_Zero_FiresOnFirstRecord`, `TestWithWindowSize_Zero_NeverFires`, `TestWithWindowSize_Negative_Panics`). Changing them is a decision for the maintainer, and `doc.go` and the README must change with it.
- `normalizeArgs` must never panic on any input (`FuzzNormalizeArgs`).
- Behavioral equivalence with Nanite is established only for what the ported tests cover: `detector.go` and `detector_test.go` were diffed byte-identical to the source and the source tests were run. Do not claim more.
- Out of scope: reflex engines, workflow continuation policy, persistence, transport, and any adoption work in an application.
