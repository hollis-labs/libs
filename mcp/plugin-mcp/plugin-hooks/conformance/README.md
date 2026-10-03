# Shared hook conformance

`execution.json` contains a Go `CatalogDocument` and dispatch scenarios.
`policy.json` contains rejected declaration, scope and registration scenarios.
Go's `TestSharedConformance` / `TestSharedPolicyConformance` and vitest's
conformance/policy tests read these same files. Hosts attach deterministic test
validators; the corpus does not compile JSON Schema.

Each execution case starts with a fresh registry/engine and one owner generation,
installs its registrations, optionally removes handles or disposes the generation,
and dispatches once for each expected result. Expected outcomes cover status,
registration order, error classes and optional filter value. Selected cases also
assert masked handler inputs, warnings or async receipts. JSON comparisons retain
number literal text and ignore object key order. Handler behaviors include noop,
fixed output, echo, ordinary error, panic, cancellation, approval, timeout and a
nested dispatch. Configuration can lower the depth bound. These are behavior
assertions, not an assertion about a particular number of declarations.

Policy cases optionally patch a declaration, create a scope with host-supplied
identity/allowlist, and preflight/register a handler. Expected errors verify the
same typed Go and TypeScript policy. TypeScript additionally checks raw catalog
JSON before constructing typed declarations; Go hosts construct `Catalog` with
compiled validators themselves, rather than decoding introspection into an
executable catalog. No aliases or implicit permissions are introduced.

Separate lifecycle tests exercise concurrent capacity/once/removal/disposal,
queue overload and cancellation using controlled completion barriers. Extend
this corpus when a shared observable execution or registration rule changes.

These scenarios complement the dispatcher requirements in `hookstest/`.
Fixture case names are independent of that package's stable R01-R15 identifiers,
which remain unchanged. The Go final gate runs both suites; the TS suite runs
the shared JSON scenarios and its lifecycle tests.
