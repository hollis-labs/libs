# go-materialize

Atomic, manifest-tracked file-tree materialization for Go: validate a whole artifact tree, write it through a private staging directory, publish with one rename, and reconcile ownership-scoped changes into an existing directory later.

It is not: a templating or scaffolding system, a provider/launch/session layer, or a place for any one caller's file layout. Callers decide what trees to write and where; this module only makes the write safe and the ownership recoverable.

## Start Here

- `README.md` — module purpose and layout; each package's `doc.go` is the full contract.
- `artifact/` — provider-independent inputs (`Entry`, `Tree`, `Digest`, `Ownership`, `Provenance`) and resolvers. Sits below everything else; it may not import `materialize`.
- `materialize/engine.go` — `DefaultEngine.Apply`: plan, stage, publish.
- `materialize/reconcile.go` — `Reconcile`/`Refresh` over mixed-ownership directories, driven by `.materialize/manifest.json`.
- `materialize/document.go` — `MergeDocument`: JSON/TOML managed-key merge.
- `materialize/contracts.go` — the request, report and error types callers depend on.
- `CHANGELOG.md` — records breaking renames from the `agentkit` original.

## Commands

```sh
go vet ./...
gofmt -l .                     # no output = clean
go test -race -count=1 ./...
golangci-lint run
govulncheck ./...
```

CI (`.github/workflows/check.yml`) runs the same checks. Run `lefthook install` once per clone; a tracked `lefthook.yml` installs no hooks by itself.

## Boundaries

- Zero runtime dependencies beyond the standard library and what `go.mod` already lists; do not add one to solve a convenience problem. No `replace` directive and no committed `go.work`: consumers cannot resolve either.
- The whole tree is validated before any destination mutation, and `Create` publishes only after every entry is written. Guards: `TestCreateFailureCasesLeaveDestinationAbsent`, `TestPlanIsDeterministicAndNonMutating`. Do not add a write path that mutates the destination before validation completes.
- Symlinks and traversal are refused: a symlink anywhere in the destination parent chain is rejected before the parent root opens, and later access stays inside `os.Root`. Guard: `TestCreateRejectsSymlinkParentAndStagedPathSwap`. A platform or mode that cannot uphold this must return an unsupported/unsafe-target error rather than weaken the boundary.
- Paths are slash-separated, relative, and case-sensitive byte for byte; no Unicode normalization or case folding. A duplicate exact path, or a file that is also a parent of another entry, is a collision. Guards: `TestResolverRejectsLimitsCancellationCollisionsAndTraversal`.
- Reconcile preserves unowned content and managed-key neighbours, preflights before mutating, and applies each file with atomic replacement. It does not claim whole-directory transactions: an interrupted multi-file write returns an incomplete report and leaves the previous manifest in place. Guards: `TestReconcilePreservesUnownedContentAndDocumentKeys`, `TestReconcileConflictsAndPartialRecovery`. Do not "fix" this into a transaction claim.
- `ExistingTargetAllowEmpty` lets `Apply(OperationCreate, ...)` publish into a pre-existing empty directory and nothing else; a populated target is still refused. Guard: `TestCreateAllowsPublishingIntoPreexistingEmptyDirectory`.
- The manifest path (`.materialize/manifest.json`), schema version (`materialize.v1`) and stage/tmp prefixes are on-disk contracts. Changing them breaks existing trees; that needs a CHANGELOG entry and a migration story.
- Source authorization is the caller's: the resolver reads only what the caller has already authorized, and that is separate from any access granted to a spawned process.
- Docs-only changes do not bump the version; behavior changes do, with a `CHANGELOG.md` entry.
