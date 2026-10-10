# Changelog

All notable changes to `llm-core/pricesource` are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- New package: price sources implementing `costcalc.PriceSource`. `Catalog` and `FromModelsDev` adapt a model catalog; `Entry`, `Rate`, `NewTable` and `FromCatalog` build override tables with explicit unknown rates; `Chain` tries sources in order; `Live` swaps a source atomically.
- `ParseTable`, `WriteJSON`, `LoadFile`, `SaveFile` and `FileCache` keep a table on disk as an offline cache; `ParseLiteLLM` reads LiteLLM-style per-token price maps.
- README migration notes for existing estimators adopting cost kinds and the price-source seam.
