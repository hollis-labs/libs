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
