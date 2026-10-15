# Changelog

## Unreleased

- MVP: `Secret`, reference grammar, `Resolver` with `Backend`/`Store`, keychain (shared service plus legacy read-through), helper, file, op and opt-in env schemes, typed errors with `Guidance`, `redact`, `credfile` (ported from Tether), `secretstest` with a real-keychain guard.
- Managed file store: `file://<ns>/<key>` under `$XDG_STATE_HOME/hollis/secrets` (0600 files, 0700 dirs, atomic writes), `FileBackend` is now a `Writer`; `secrets.StateDir`, `WithStateDir`.
- `cmd/secrets`: `set` (stdin only, refuses a terminal), `check` (source, never the value), `delete`.
- `keychain://` on a Linux host without Secret Service returns `unavailable` with guidance to `file://` / `op://`.
- Behaviour change: `file://relative/path` is no longer invalid; it is a managed entry (`file://<ns>/<key>`).
