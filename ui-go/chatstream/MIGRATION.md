# Migration: go-chatstream -> libs/ui-go/chatstream

`github.com/hollis-labs/go-chatstream` moved into the libs monorepo as `ui-go/chatstream/`, with its full git history (11 commits, 2026-09-30 to 2026-09-30; source HEAD `dce462ff8541`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-chatstream` | `github.com/hollis-labs/libs/ui-go/chatstream` | `chatstream` |
| `github.com/hollis-labs/go-chatstream/adapter/acp` | `github.com/hollis-labs/libs/ui-go/chatstream/adapter/acp` | `acp` |
| `github.com/hollis-labs/go-chatstream/adapter/anthropic` | `github.com/hollis-labs/libs/ui-go/chatstream/adapter/anthropic` | `anthropic` |
| `github.com/hollis-labs/go-chatstream/adapter/claudejson` | `github.com/hollis-labs/libs/ui-go/chatstream/adapter/claudejson` | `claudejson` |
| `github.com/hollis-labs/go-chatstream/adapter/codexjson` | `github.com/hollis-labs/libs/ui-go/chatstream/adapter/codexjson` | `codexjson` |
| `github.com/hollis-labs/go-chatstream/adapter/openaichat` | `github.com/hollis-labs/libs/ui-go/chatstream/adapter/openaichat` | `openaichat` |
| `github.com/hollis-labs/go-chatstream/adapter/openairesponses` | `github.com/hollis-labs/libs/ui-go/chatstream/adapter/openairesponses` | `openairesponses` |
| `github.com/hollis-labs/go-chatstream/conformance` | `github.com/hollis-labs/libs/ui-go/chatstream/conformance` | `conformance` |
| `github.com/hollis-labs/go-chatstream/conformance/crosscheck` | `github.com/hollis-labs/libs/ui-go/chatstream/conformance/crosscheck` | `crosscheck` |
| `github.com/hollis-labs/go-chatstream/conformance/fakeserver` | `github.com/hollis-labs/libs/ui-go/chatstream/conformance/fakeserver` | `fakeserver` |
| `github.com/hollis-labs/go-chatstream/conformance/timing` | `github.com/hollis-labs/libs/ui-go/chatstream/conformance/timing` | `timing` |
| `github.com/hollis-labs/go-chatstream/examples/decode` | `github.com/hollis-labs/libs/ui-go/chatstream/examples/decode` |  (command) |
| `github.com/hollis-labs/go-chatstream/framing` | `github.com/hollis-labs/libs/ui-go/chatstream/framing` | `framing` |
| `github.com/hollis-labs/go-chatstream/hubbind` | `github.com/hollis-labs/libs/ui-go/chatstream/hubbind` | `hubbind` |
| `github.com/hollis-labs/go-chatstream/sink` | `github.com/hollis-labs/libs/ui-go/chatstream/sink` | `sink` |
| `github.com/hollis-labs/go-chatstream/sink/agui` | `github.com/hollis-labs/libs/ui-go/chatstream/sink/agui` | `agui` |
| `github.com/hollis-labs/go-chatstream/sink/aisdk` | `github.com/hollis-labs/libs/ui-go/chatstream/sink/aisdk` | `aisdk` |
| `github.com/hollis-labs/go-chatstream/sink/nanitelegacy` | `github.com/hollis-labs/libs/ui-go/chatstream/sink/nanitelegacy` | `nanitelegacy` |
| `github.com/hollis-labs/go-chatstream/sink/native` | `github.com/hollis-labs/libs/ui-go/chatstream/sink/native` | `native` |
| `github.com/hollis-labs/go-chatstream/sink/openaicompat` | `github.com/hollis-labs/libs/ui-go/chatstream/sink/openaicompat` | `openaicompat` |
| `github.com/hollis-labs/go-chatstream/sink/sinktest` | `github.com/hollis-labs/libs/ui-go/chatstream/sink/sinktest` | `sinktest` |

`go get github.com/hollis-labs/libs/ui-go/chatstream@<version>` replaces `go get github.com/hollis-labs/go-chatstream@<version>`; the new module is `github.com/hollis-labs/libs/ui-go`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/ui-go` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `ui-go/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Internal requirements.** It required `go-ssekit` and `go-streamhub` as separate modules; those are now ordinary imports of `github.com/hollis-labs/libs/ui-go/ssekit` and `.../ui-go/streamhub` inside the same module.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`, `go.sum`.
