// Package pricesource provides price sources for costcalc.PriceSource: a
// model-catalog adapter (models.dev), override tables with explicit unknown
// rates, a parser for LiteLLM-style per-token price tables, a chain that
// tries sources in order, and an offline file cache for tables.
//
// Every source returns a usageledger.PriceSnapshot with Source and AsOf set
// as far as the source knows them, and with UnknownRates marking each rate
// the source does not have, so a missing rate prices as partial rather than
// free. A source never multiplies rates against usage; costcalc does that.
//
// The package does no network I/O. A models.dev client keeps its own disk
// cache (modelsdev.WithCacheDir); a table is fetched by the caller and kept
// offline with FileCache.
package pricesource
