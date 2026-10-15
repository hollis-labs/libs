// Package llmcore is the root package of the llm-core module. It holds no code:
// the module's API lives in its packages.
//
//   - llmtypes: transport-agnostic request, response, tool and stream-event types.
//   - llmcontracts (and contracttest): the provider interface, its optional
//     extensions and rate-budget helpers.
//   - embedcontracts: text-embedding interfaces and data shapes.
//   - usageledger: the disjoint LLM token-usage record.
//   - modelsdev: a cached client for the models.dev model catalog.
//   - costcalc: prices a usageledger Usage with modelsdev Pricing.
//   - guard: call admission for LLM resources: one circuit breaker, per-key
//     cooldowns with provider retry-after, and error classification.
//   - pricesource: price sources for costcalc (models.dev, override tables,
//     LiteLLM-style tables, an offline cache).
//   - contracts (with capabilities and runtimes): the shared contract types for
//     launching an agent.
//   - quota (and quotatest): usage limits over calendar, rolling and
//     provider-reported windows, with Check / Reserve / Commit and pluggable
//     stores.
//
// Each package directory carries its own README.md, AGENTS.md and CHANGELOG.md from
// the repository it was imported from, and a MIGRATION.md with the old and new
// import paths. Packages written in this module have at least a README.md. The
// module is released with module-prefixed tags of the form llm-core/vX.Y.Z.
package llmcore
