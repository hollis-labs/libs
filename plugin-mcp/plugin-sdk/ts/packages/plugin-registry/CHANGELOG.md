# @hollis-labs/plugin-registry

## 0.1.0 — 2026-09-29

First npm release. Wire protocol 1.

Until now the package was reachable only by a `file:` path into a checkout of
this repository (plugin-sdk v0.4.0 and v0.5.0 shipped it that way), which broke
any consumer whose CI checks out its own repo alone. Nothing in `src/` changed
for this release: the loader, the wire types (`PluginRegistryResponse` and
friends), the `./react` entry point and the stylesheet sinks are the code those
tags carried. What changed is the packaging — the package is no longer private,
the tarball ships `dist`, `README.md`, `CHANGELOG.md` and `LICENSE`, and the
source and declaration maps are no longer emitted because they pointed at a
`../src` the tarball does not contain.
