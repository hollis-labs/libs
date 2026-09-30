# Changelog

All notable changes to go-usage-ledger are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- Initial extraction: `Provenance`, `Component`, `Usage` (five disjoint core components plus `Dims`), `NewUsage`, `Usage.SetDim`, `Usage.TotalTokens`, `Usage.TotalProvenance`, `Usage.Validate`, `PriceSnapshot` and `Row`. Standard library only.
- Tests for the stored-total and unreported-as-zero defects, JSON round trip, and `FuzzValidate`.
