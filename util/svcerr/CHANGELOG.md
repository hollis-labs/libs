# Changelog

All notable changes to go-svcerr are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- `Error` (status, code, safe message, field, internal cause) with `Unwrap` and a code-matching `Is`; `New`, `Wrap`, `WithField`, `WithStatus`.
- Six built-in codes (`invalid`, `not_found`, `conflict`, `permission`, `unavailable`, `internal`) with default HTTP statuses, `DefaultStatus`, and matching sentinels for `errors.Is`. `Code` is an open string type.
- `StatusFor` and `CodeFor`, which read the first `*Error` in a chain and never inspect error text.
- Optional `WriteJSON` and `Envelope` (`{"error":{"code","message","field"?}}`, Tether's nesting), in their own file. A non-`*Error` is written with the caller's fallback status and a generic body, never its own text.
- Hardening beyond the obvious shape: sentinels carry their code's status (a bare `&Error{}` literal would have yielded status 0), an unset or out-of-range status falls back to the code's default and then to 500 (so `WriteHeader` cannot panic), and an empty message falls back to the status text.
