# Changelog

All notable changes to go-tesseract-client are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## v0.1.0 — 2026-09-29

### Added

- `tesseract` package: an HTTP client for Tesseract's memory API covering `Recall`, `RecallAll`, `GetCurrent`, `GetRevision`, `Deprecate`, `ListNamespaces` and `Health`, with `APIError`, `ErrNotFound`, `ErrUnavailable`, `ErrResponseTooLarge`, and `WithTimeout` / `WithMaxResponseBytes` (defaults 20s / 64 MiB).
- `RecallAll` and `ListNamespaces` refuse any cursor they have already used, not only one repeated back to back. A server or proxy answering A, then B, then A used to send `ListNamespaces` into an ever-growing result and `RecallAll` into an endless loop over empty pages; both now return a "repeated cursor" error. `ListNamespaces` additionally stops with an error after 1000 pages, for a server whose cursors never repeat but never end.
- `RecallAll` with `maxRecords <= 0` fetches and returns the first page only (`complete` is false if the server has more); this is now documented and tested.
- `RecallFilters` and `RecallRequest` matching the server's recall contract, including `search_mode` and `filters` together. Filter keys use the server's Go field names.
- `tesseracttest` package: an importable fake Tesseract with call counting, request recording, and failure, auth, paging and delay knobs.
- Response size cap is enforced as an error rather than a truncated read.
