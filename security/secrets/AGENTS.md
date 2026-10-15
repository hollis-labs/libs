# go-secrets

Secret references, a non-leaking `Secret` type, typed errors, redaction and a test kit for the Hollis Labs apps. Nothing here may be a policy layer: read/write bindings, audit storage and wire error codes belong to the apps.

## Start Here

- `secret.go` — `Secret`; `ref.go` — grammar; `errors.go` — `Kind`, `Error`; `resolver.go` — `Resolver`, `Backend`, `Store`; `options.go` — `New` options and wiring.
- `backend_*.go`, `store_keyring.go`, `exec.go` — the schemes and the helper runner.
- `credfile/`, `redact/` — standalone ports from Tether (no dependency on the root package).
- `secretstest/` — the kit; `Install(t)` first in any test that could reach the keychain.

## Commands

```sh
gofmt -l .
go vet ./...
golangci-lint run ./...
go test -race -count=1 ./...
```

## Boundaries

- No `replace` directive and no committed `go.work`. The only third-party dependency is `zalando/go-keyring`.
- A `Secret` never prints its value. Guarded by `TestSecretNeverPrintsItsValue` and `secretstest.AssertNoLeak`.
- An `Error` never renders its cause, helper stderr or the input of a failed parse. Guarded by `TestErrorTextNeverRendersTheCauseButKeepsItReachable`, `TestHelperExitCodesMapToKinds`, `TestParseRefRejectsAndNeverEchoesItsInput`, `TestUnsupportedSchemeAndLiteralNeverEchoCredentials`.
- Tests never reach the real keychain: `NewKeyringStore` refuses inside a test binary until `MockKeyring`. Guarded by `TestRealStoreIsRefusedInsideATestBinaryUntilMocked`.
- The environment is never a silent fallback. Guarded by `TestEnvIsOffByDefaultAndExplicitWhenOn` and `TestEnvFallbackIsOptInAndReportsItsSource`.
- Legacy keychain locations are read, never written. Guarded by `TestKeychainReadsThroughToLegacyNamesWithoutMigrating`.
- A helper's child environment has reference-valued variables removed. Guarded by `TestHelperChildNeverSeesReferenceValuedVariables`.
- Do not copy `redact` or `credfile` behaviour into an app; extend them here.
