# App host

Reusable Go HTTP and embedded SPA assembly, with reviewed plugin registry delivery and process inventory.

Import `github.com/hollis-labs/libs/app/app-host/host` for HTTP assembly and `github.com/hollis-labs/libs/app/app-host/plugins` for plugin delivery and inventory. Shared public DTOs come from `libs/mcp/plugin-mcp/plugin-sdk` and `libs/mcp/plugin-mcp/plugin-host` v0.3.0; embedded web UI comes from `libs/ui/ui-go` v0.1.0. Requires Go 1.26.9.

Consumers own product policy, discovery, credentials, identity, listeners and application effects. The library preserves exact child environments, reviewed bundle bytes, owner-generation fencing and bounded HTTP-first shutdown. It does not supply an application binary, frontend, fixture catalogue, authentication policy or deployment configuration.

Source: the `host/` and `plugins/` packages from [Chimera commit 77113f4](https://github.com/hollis-labs/chimera/tree/77113f4b08b6cbd6ddbf20c4c785d2f1bf0e6c2b), including their tests. Package history is imported with the repository import tooling; only shared dependency paths change. Chimera's lab application and frozen consumer recipe are excluded.

From the repository root, run `scripts/check -race app/app-host` and the layout, dependency-direction and public-hygiene guards. Releases use `app/app-host/vX.Y.Z` tags.
