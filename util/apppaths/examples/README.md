# go-apppaths examples

Runnable examples for the `paths` package — one `main` package per
subdirectory.

| Example | What it shows |
|---|---|
| [`printpaths/`](./printpaths) | Resolve a layout and print every entry via `Layout.Describe()` — the data behind an `<app> path` introspection subcommand. Uses `WithoutMaterialize`, so it has no filesystem side effects. |

```sh
go run ./examples/printpaths
```
