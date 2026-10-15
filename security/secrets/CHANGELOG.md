# Changelog

## v0.1.0 — 2026-10-15

Imported with history from the private go-secrets repository (`feat/mvp`); now public here.


- MVP: `Secret`, reference grammar, `Resolver` with `Backend`/`Store`, keychain (shared service plus legacy read-through), helper, file, op and opt-in env schemes, typed errors with `Guidance`, `redact`, `credfile` (ported from Tether), `secretstest` with a real-keychain guard.
- Managed file store: `file://<ns>/<key>` under `$XDG_STATE_HOME/hollis/secrets` (0600 files, 0700 dirs, atomic writes), `FileBackend` is now a `Writer`; `secrets.StateDir`, `WithStateDir`.
- `cmd/secrets`: `set` (stdin only, refuses a terminal), `check` (source, never the value), `delete`.
- `keychain://` on a Linux host without Secret Service returns `unavailable` with guidance to `file://` / `op://`.
- Behaviour change: `file://relative/path` is no longer invalid; it is a managed entry (`file://<ns>/<key>`).
- `tokenfile` package (ported from go-tether-client's reader and Hadron's `localauth/token.go`): `Read` (final-component O_NOFOLLOW, regular file, mode exactly 0600, euid owner, size cap, optional validator), `Generate`, `Create` (O_EXCL), `Ensure`, atomic `Write`/`Rotate` (temp, fsync, rename, dir sync), `Watcher` (`Token`, `Verify`; stat-stamp re-read, digest compare, fail closed), `RefreshIfOlder` and `Keep` (refresh-in-place for short-lived tokens), `Equal`/`Digest`.
- macOS argv: go-keyring v0.2.8 writes through `security -i` on stdin, so the secret is not in argv (source-verified, not run on macOS); documented in the README. Tether's helper has its own argv-exposing darwin write (CW-20261015-0014).

### Not yet

- `Exec` shim (resolve then exec; Cerberus `run-secrets`).
- Caching.
- `Field` config type (authored-scheme-only).
- Pattern redaction and tail-mode redaction.
- SDK-based `op` (the 1Password CLI is used).
