# go-apppaths examples

Runnable examples for the `paths` package — one `main` package per
subdirectory.

| Example | What it shows |
|---|---|
| [`printpaths/`](./printpaths) | Resolve a layout and print every entry via `Layout.Describe()` — the data behind an `<app> path` introspection subcommand. Uses `WithoutMaterialize`, so it has no filesystem side effects. |
| [`ownerperms/`](./ownerperms) | Owner-only permissions: leaves an app root at a loose `0755` under a throwaway `$HOME`, resolves, and prints the retightened `0700`. Touches only its own temp directory. |

```sh
go run ./examples/printpaths
go run ./examples/ownerperms
```
