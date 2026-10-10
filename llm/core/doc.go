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
//   - contracts (with capabilities and runtimes): the shared contract types for
//     launching an agent.
//
// Each package directory carries its own README.md, AGENTS.md and CHANGELOG.md from
// the repository it was imported from, and a MIGRATION.md with the old and new
// import paths. The module is released with module-prefixed tags of the form
// llm-core/vX.Y.Z.
package llmcore
