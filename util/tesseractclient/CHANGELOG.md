# Changelog

All notable changes to go-tesseract-client are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- `tesseract` package: an HTTP client for Tesseract's memory API covering `Recall`, `RecallAll`, `GetCurrent`, `GetRevision`, `Deprecate`, `ListNamespaces` and `Health`, with `APIError`, `ErrNotFound`, `ErrUnavailable`, `ErrResponseTooLarge`, and `WithTimeout` / `WithMaxResponseBytes` (defaults 20s / 64 MiB).
- `RecallFilters` and `RecallRequest` matching the server's recall contract, including `search_mode` and `filters` together. Filter keys use the server's Go field names.
- `tesseracttest` package: an importable fake Tesseract with call counting, request recording, and failure, auth, paging and delay knobs.
- Response size cap is enforced as an error rather than a truncated read.
