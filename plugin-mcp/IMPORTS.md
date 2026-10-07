# Import provenance

These package trees retain the commit histories of their standalone repositories. The sources below were read at exact commits; the final module rewrites their Go imports to `github.com/hollis-labs/libs/plugin-mcp/<package>`. Original module manifests remain in history. Their external requirements were combined at the recorded versions before module tidy; internal dependencies now resolve within this module.

| Package | Original repository | Imported source commit |
| --- | --- | --- |
| `plugin-sdk` | [hollis-labs/plugin-sdk](https://github.com/hollis-labs/plugin-sdk) | `ff312e20f9939d9e1cb2d5dcd786c52565da47b4` |
| `plugin-host` | [hollis-labs/plugin-host](https://github.com/hollis-labs/plugin-host) | `60aa3a0217bba1a6c10af9a09af5a06554b939f4` |
| `mcp-host` | [hollis-labs/mcp-host](https://github.com/hollis-labs/mcp-host) | `e1d9650926d27e4a8e24e9ab883a2c42ee5c913b` |
| `go-mcp` | [hollis-labs/go-mcp](https://github.com/hollis-labs/go-mcp) | `dbc54734b3964def661bd15560cbc9051b1c2518` |
| `go-mcp-sanitize` | [hollis-labs/go-mcp-sanitize](https://github.com/hollis-labs/go-mcp-sanitize) | `0cd2b95a213c906f6f03b82b927840fd185f0c89` |
| `api-projection` | [hollis-labs/api-projection](https://github.com/hollis-labs/api-projection) | `5a2b645e019a7a98425ec9c5f16d502e53e28b65` |

The import does not copy standalone tags into this repository. Go consumers use the single `plugin-mcp/vX.Y.Z` module tag. Existing releases of the standalone repositories remain historical; plugin hooks retain their independent repository and version. The first final-home family tag is prepared as `plugin-mcp/v0.1.0` and requires the reviewed release commit before publication.

Original MIT license files remain in each package tree, and the module has its own [LICENSE](LICENSE). TypeScript npm package names remain unchanged.

Source consolidation does not prove application policy, backend authorization, native/runtime conformance or live adoption. Historical test reports describe their recorded source and assets; import-path changes do not turn them into new executions.
