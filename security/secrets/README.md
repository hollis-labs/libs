# go-secrets

Secret references, a `Secret` type that cannot leak, typed errors, redaction and a test kit, shared by the Hollis Labs apps.

## Status

**Pre-release MVP.** Unreleased, not deployed, no consumers yet: no app has migrated to it. Interfaces change without notice. See [CHANGELOG.md](./CHANGELOG.md).

## Install

```sh
go get github.com/hollis-labs/libs/security/secrets
```

## Usage

```go
r := secrets.New(secrets.WithRedactor(&known))      // keychain, helper, file, op
res, err := r.Resolve(ctx, "keychain://openai/work") // res.Secret prints as [redacted]
key := res.Secret.Reveal()                           // only at the call that needs it
```

See `example_test.go` for a runnable version.

## References

The grammar is always `<scheme>://<authority>[/<path>...]`.

| Scheme | Meaning | Source reported |
|---|---|---|
| `keychain://<ns>/<key>` (alias `keyring://`) | OS credential store, service `hollis-labs`, account `<ns>/<key>` | `keychain`, `keychain:legacy:<svc>` |
| `helper://<name>/<rest>` | runs `<name> resolve keychain://<rest>` | `helper:<name>` |
| `file:///abs`, `file://~/p` | private credential file (`credfile` rules); read-only | `file` |
| `file://<ns>/<key>` | managed file store, `0600` in a `0700` tree under `$XDG_STATE_HOME/hollis/secrets` (go-apppaths `Resolve("hollis").StateDir()/secrets`). Readable and writable. **The default on headless Linux.** | `file` |
| `op://vault/item/field` | `op read --no-newline` (1Password CLI) | `op` |
| `env://VAR` | environment variable. **Off unless `WithEnv()`.** | `env:VAR` |

Another scheme (Vault, Infisical, a secrets service of our own) is a `Backend` added with `WithBackend`. The grammar does not change.

### Helper protocol v0

Exit 0 with the value on stdout; exit 2 not found; exit 3 denied; any other exit is unavailable. stderr is discarded, never read. The child's environment has every variable removed whose value is a reference. The helper name is a bare executable name (PATH, then `GOBIN`, `~/go/bin`, `~/.local/bin`, the executable's directory, `/opt/homebrew/bin`, `/usr/local/bin`). Output is capped at 64 KiB.

## Headless Linux: the file store and `secrets set`

Fleet hosts have no Secret Service, so `keychain://` returns `unavailable` there, with guidance pointing at `file://<ns>/<key>` or `op://`. The managed file store needs nothing running:

```sh
go install github.com/hollis-labs/libs/security/secrets/cmd/secrets@latest
printf %s "$KEY" | secrets set file://openai/work   # stdin only, never argv
secrets check file://openai/work                    # prints the source, never the value
```

Writes are atomic (temp file, fsync, rename). Segments are plain names (no `..`, no leading `.`). In code: `r.Set(ctx, ref, reader)`, `r.Delete`, `secrets.StateDir()`, `secrets.WithStateDir(dir)`. Path-form `file:///abs` references are never written by this package.

## Errors

Every error is a `*secrets.Error` with a stable `Kind`: `not_found`, `denied`, `unavailable`, `invalid`, `empty`, `resolved_to_ref`, `too_large`, `unsupported_scheme`. `errors.Is(err, secrets.ErrNotFound)` matches by kind; `Code()` is a wire string such as `secret_not_found`; `Guidance()` says what to do. `Error()` never renders the cause, a helper's stderr or the input of a failed parse; `Unwrap()` still reaches the cause.

## Environment policy

The environment is not a silent fallback. Use `env://VAR` (with `WithEnv()`), or opt in with `WithEnvFallback(names)`: it is tried only after not found or unavailable (never after denied) and the result says `Source: env:VAR`.

## Migrating from per-app keychain names

`WithLegacy(service, account)` reads an old location when the shared service has no entry. Nothing is copied and nothing is written to the old location. Writes go to the shared service only.

## Packages

- `secrets` (root): `Secret`, `Ref`, `Resolver`, `Backend`, `Store`, the schemes, `Error`.
- `redact`: value redaction set (raw, URL-escaped and JSON-escaped forms; `Remember`; `Error` wrapper). Whole-text only.
- `credfile`: the hardened credential-file reader.
- `tokenfile`: bearer-token file lifecycle (strict `Read`, `Create`/`Ensure`, atomic `Write`/`Rotate`, `Watcher` with re-read on change and digest compare, `RefreshIfOlder`/`Keep` for short-lived tokens). Standard library only.
- `secretstest`: `Install`, `FakeStore`, `FakeBackend`, `WriteHelper`, `AssertNoLeak`.

## macOS and argv

Verified by reading `zalando/go-keyring` v0.2.8 `keyring_darwin.go` (not run on a macOS host, none available here): `Set` starts `/usr/bin/security -i` and writes `add-generic-password -U -s ... -a ... -w <value>` to the child's **stdin**. The secret is therefore not on any process's command line; argv is just `security -i`. `Get` uses `find-generic-password -wa`, which prints to stdout. So `keychain://` writes through this package keep the value off argv, and no replacement path is needed. Residual limits: the value is base64-wrapped (`go-keyring-base64:`), which is an encoding, not protection; the whole command is capped at 4096 bytes, so a value over about 3 KB is `too_large`; and in v0.2.8 the cap is checked after the child has started. Tether's own `tether-apikey-helper` darwin `set` does call `security add-generic-password ... -w <secret>` directly and does expose the secret in argv: moving it onto this package (or `security -i`) removes that (CW-20261015-0014).

## Not yet

`Exec` shim (resolve, then exec; Cerberus `run-secrets`); caching; `Field` config type (authored-scheme-only: a value that becomes a reference only after `${VAR}` expansion stays literal); tail-mode redaction for cut stderr; pattern redaction (`sk-`, Bearer, PEM); SDK-based `op` (the CLI is used). Headless Linux uses the managed file store; there is no `pass` or encrypted-file fallback.

## Limits worth knowing

Same-uid processes can read the environment and any file you can read; this library does not defend against them. Redaction is best effort for text the process did not compose.
