# Changelog

This module is versioned independently with `security/redact/vX.Y.Z` tags.

## Unreleased

## v0.2.0 — pending security and performance gates

### Added

- Immutable per-kind `Sanitizer` and `Policy`, typed sensitivity/error classes,
  value-free reports with JSON pointers, default allowlist mode and explicit
  denylist mode. Sensitive key policy still wins over an allowed path.
- Deterministic email, phone, IP, Luhn-card and Nanite identifier PII rules on
  the existing span engine, with classifications retained before overlap merging.
- Fail-closed JSON tombstones for size, format, parse and depth errors;
  duplicate keys and invalid UTF-8 are rejected.
- `ErrorClasses()` returns an independently owned copy of the closed sanitizer
  failure-label set for consumers that recognize fail-closed tombstones.
- Seeded event payload corpus, concurrency/idempotence/fuzz checks, and a
  4 KiB latency/allocation benchmark reporting sampled wall-clock p99.

### Changed

- Normalize structured key names (strip format characters, then NFKC), and
  recognize joined secret-key words. Adds the pinned x/text normalization dependency.
- Mask all non-allowlisted report key segments, classify dropped fields from
  inspection only, and exempt correlation/digest metadata from the 32-hex rule alone.
- PII recognizes percent encoding, sentence-final IPv6 and adjacent national phones.
- Provider-key patterns scan candidate tokens instead of the entire text;
  reference-span checks retain the existing pattern semantics.

## v0.1.0 — 2026-10-16

### Added

- `Redactor.Writer(dst, ...WriterOption)`: a streaming redactor. It emits whole lines, so the output equals `Text` of the whole stream however the input is split across `Write` calls (tested byte at a time, at every split of short inputs, and with random chunking). An open match at the end of the buffer (an unterminated private-key block) and a known value that spans lines are held until they close. Memory is bounded: `WithMaxPending` (default 64 KiB) forces a flush of an unbroken line, keeping `WithHold` (default 4 KiB) back; a value longer than the hold that straddles a forced flush can be missed. `Close` flushes and does not close `dst`.
- `Redactor` (`New`, `Default`, `Text`, `TextCount`, `Bytes`, `Scan`, `Tail`) over a span engine: every rule finds ranges, ranges are merged and replaced in the original text, so rules compose and redacting twice equals redacting once.
- Rules: private-key blocks (including truncated), provider key shapes, webhook URLs, `Authorization` headers (scheme kept), bare `Bearer`, cookies, URL userinfo, DSN passwords, secret query parameters, key/value assignments (env, JSON, YAML, logfmt, percent-encoded, JSON inside JSON), command-line flags, `curl -u`, `sshpass -p`, `mysql -p`, `config set`.
- `Scope`: known secret values and their URL-, JSON-, HTML-, Go-quoted and base64 forms; minimum length with `Unprotected()` reporting; concurrency-safe.
- Whole-word key policy (`SensitiveName`) with benign suffixes, plural counts, secret references and `WithBenignKeys` / `WithSensitiveKeys`.
- Structured paths: `JSON`, `Marshal`, `MarshalIndent` (valid JSON, key order and numbers kept), `Args`, `Env`.
- `Guidance` / `GuidanceWrap` / `ErrorText` / `Error`: application prose is kept, a wrapped cause is redacted; `errors.Is` and `errors.As` still work.
- `Handler`: `log/slog` handler.
- Normalization: zero-width and format characters are dropped and fullwidth ASCII folded in the view the rules read; the original text is what is replaced.
- Options: `WithMarker`, `WithScope`, `WithRule` (invalid pattern is an error), `WithoutRules`, `WithReferenceSchemes`, `WithHeuristics`.
- Conformance corpus, `FuzzText`, and a pathological-input test.
