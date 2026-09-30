# Changelog

All notable changes to go-transportparity are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- `Outcome`, `FromValue`, `FromError` (category and status derived from a `*svcerr.Error` in the chain, else the caller's fallbacks) and `AssertSameOutcome`: both succeed or both fail; on failure the same non-empty category; on success the same `CanonicalJSON`. Status and error text are never compared.
- `CanonicalJSON`: sorted keys at every depth, normalized numbers (`1`, `1.0` and `1e0` agree; integers keep full precision), pointer and value equivalent.
- `HTTPJSON`: invoke an `http.Handler` directly and decode a JSON body; a non-JSON body is reported.
- `AcceptedFieldNames` and `AssertSameFieldNames`: compare the JSON field paths two request shapes accept. Lifted from the pure-reflection walker in Tesseract's own request-parity test, with two corrections: it flattens untagged embedded structs the way `encoding/json` does (and still walks an unexported embedded struct), and it returns nil for a nil argument instead of panicking. The results are sorted.
- The assertions take a small `T` interface (satisfied by `*testing.T` and `testing.TB`) so failure paths are testable.
