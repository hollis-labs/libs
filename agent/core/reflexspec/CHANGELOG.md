# Changelog

All notable changes to reflex-definition-spec are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- `Definition`, `Validate`, `ValidateTriggerSpec`, `ValidateActionSpec` and the `*Error` type with stable codes.
- The go-reflexes v0.1.0 predicate grammar (AND, OR and 14 leaf kinds) as data, `PredicateKinds`, and a walk that rejects unknown kinds, missing required fields, bad operators, bad RE2 patterns and unknown fields.
- Per-kind `action_spec` checks for `inject_reminder`, `halt_session`, `dispatch_to_agent` and `resume_loop_run`; object-only checks for every other kind.
- Open enums with optional catalogs: `WithKnownActionKinds`, `WithKnownStatuses`, `WithKnownProvenanceTiers`, `WithKnownEvents`.
- `conformance` package: embedded valid and invalid fixtures and a reference runner.
- Fuzz tests for the validator.

### Adoption notes for Nanite

- Six of Nanite's 21 seeded reflexes use its `scope_tier` / `execution_pattern` predicate kinds. This spec targets go-reflexes' grammar, where both are the generic `attr` predicate, so it rejects them as unknown kinds. Translate them to `attr` before validating.
- Nanite's PATCH endpoint treats `recurrence_override_seconds: 0` as "clear the override". go-reflexes and this spec treat `0` as an explicit no-cooldown and `nil` as "inherit the kind default". Nanite's adoption must map its API behavior onto that.
