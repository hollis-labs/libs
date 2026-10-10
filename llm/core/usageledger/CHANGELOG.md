# Changelog

All notable changes to go-usage-ledger are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## [Unreleased]

### Added

- `CostKind` (`api_billed`, `api_estimated`, `subscription_equivalent`, `local_compute`, and the unspecified zero value) with `Valid` and `IsBill`, and `Row.CostKind` (`cost_kind`, omitted when empty).
- `PriceSnapshot.Source`, `AsOf` and `UnknownRates` (omitted when empty), `PriceSnapshot.RateKnown` and `Validate`, `Row.Validate`, and `CoreComponentNames`.

### Compatibility

- Backwards compatible on the wire: a row without the new fields decodes as `CostKindUnspecified` with every rate known and re-encodes unchanged.

## v0.1.0 — 2026-09-29

### Added

- Initial extraction: `Provenance`, `Component`, `Usage` (five disjoint core components plus `Dims`), `NewUsage`, `Usage.SetDim`, `Usage.TotalTokens`, `Usage.TotalProvenance`, `Usage.Validate`, `PriceSnapshot` and `Row`. Standard library only.
- Tests for the stored-total and unreported-as-zero defects, JSON round trip, and `FuzzValidate`.
