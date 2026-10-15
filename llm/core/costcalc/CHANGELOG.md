# Changelog

All notable changes to go-modelsdev-catalog-helpers are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## [Unreleased]

### Added

- `PriceSource` interface and `PriceFromSource`; implementations live in `llm-core/pricesource`.
- `Cost.Kind` (copied from the row by `PriceRow`), `Cost.UnpricedTokens`, `Cost.Partial` and `Cost.IsBill`.

### Changed

- `Price` counts tokens in components whose rate the snapshot marks unknown, and all `Dims` tokens, in `UnpricedTokens`. Their dollar contribution is unchanged ($0).

## v0.1.0 — 2026-09-29

### Added

- Initial extraction: `Catalog`, `Cost` (with derived `Total`), `Price`, `PriceModel`, `PriceRow`, `SnapshotPrice` and `PriceSnapshotFromPricing`, pricing all five usage components. Depends on `go-usage-ledger` v0.1.0 and `go-modelsdev` v0.2.0.
- Regression tests for the input/output-only and fold-then-price cost shapes, the found-but-free model, the stored-snapshot rule, and `Pricing`/`PriceSnapshot` field parity; `FuzzPrice`.
