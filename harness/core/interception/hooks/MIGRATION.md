# Import migration

The preserved eleven-event hooks contract is part of
`github.com/hollis-labs/substrate/harness` starting at v0.4.0.

```sh
go get github.com/hollis-labs/substrate/harness@v0.4.0
```

| Previous import | Published import |
|---|---|
| `github.com/hollis-labs/go-hooks` | `github.com/hollis-labs/substrate/harness/interception/hooks` |
| `github.com/hollis-labs/go-hooks/cmdhook` | `github.com/hollis-labs/substrate/harness/interception/hooks/cmdhook` |
| `github.com/hollis-labs/go-hooks/conformance` | `github.com/hollis-labs/substrate/harness/interception/hooks/conformance` |

Update imports and remove the old module requirement when it is unused.
The contract, command runner and fixtures retain their existing behavior.
The root package has no I/O or third-party dependencies; command execution
remains explicitly host-owned and unsandboxed. This move does not install or
execute hooks during boot and does not grant effective permission or trust.

`github.com/hollis-labs/plugin-hooks` remains a separate catalog engine with
a different contract. Its API is not replaced by this import, and its users
should not rewrite imports to this package. The archived source repository,
old tags and historical changelog remain available. Module releases now use
`harness/vX.Y.Z`; no nested hooks module or hooks-specific tag exists.
