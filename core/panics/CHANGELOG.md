# Changelog

This module is versioned independently with `core/panics/vX.Y.Z` tags.

## Unreleased

### Added

- `Guard`, `Config`, `Record`, `Error`: panic containment that returns an error carrying a ref
  and nothing from the panic; the redacted, bounded record goes to a `Reporter`.
- `Go`, `Call`, `Recover`, `Capture` (package-level on a default guard, and methods on `Guard`).
- The panic value is withheld unless a `Redact` function is configured (`Unredacted` opts out
  explicitly); a failing redactor withholds instead of recording raw text.
- `Clean`: invalid UTF-8 repaired, control and format characters replaced, bounded without
  splitting a rune.
- `StderrReporter`, `Multi`, `ReporterFunc`; `With` attributes; `ExitSoftware` (70).
- `httpguard.Handler`: net/http middleware (500 + ref), keeps `http.ErrAbortHandler`,
  `http.Flusher` and `http.ResponseController` working.
