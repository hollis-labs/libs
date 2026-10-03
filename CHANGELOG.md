# Changelog

Repository-level changes to `libs`: tooling, CI, documentation and layout.
Changes to a module's code are in that module's own `CHANGELOG.md`, because each
module is versioned and released independently with tags of the form
`<module>/vX.Y.Z`.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
There is no repository-wide version.

## [Unreleased]

### Added

- Four empty module skeletons: `util`, `ui-go`, `workflow` and `plugin-mcp` (a
  held stub), each with its own `go.mod` (Go 1.26.6) and a package doc.
- `scripts/check`: gofmt, vet, build and test per module, always with
  `GOWORK=off`.
- `scripts/check-layout`: fails on a committed `go.work`, any `replace`
  directive, a `go.mod` outside `<module>/go.mod`, or a module path that does
  not match its directory.
- `scripts/check-one-way`: fails when `go.mod`, `go.sum`, `go.work` or Go imports
  reference `github.com/hollis-labs/substrate`. Substrate may depend on libs,
  never the reverse.
- `scripts/release`: validates a module release and prints its module-prefixed
  tag. Dry run by default; creates a local annotated tag only with `--apply`;
  never pushes.
- `scripts/import-repo`: history-preserving import of an existing repository
  into a module subdirectory with `git filter-repo`. Old tags are not carried;
  the imported `go.mod` requirements are reported.
- `scripts/scan-public`: scans the working tree and the full history for
  secrets, internal hosts and addresses, and private paths; runs `gitleaks` too
  when installed.
- CI: one path-filtered workflow per module through a shared `_module.yml`
  (gofmt, `go mod tidy -diff`, build, vet, test; the race detector everywhere
  except the `plugin-mcp` stub), and a `guards` workflow for the layout, the
  one-way rule and the public-hygiene scan.
- README, AGENTS.md, CONTRIBUTING, SECURITY and the MIT LICENSE.
