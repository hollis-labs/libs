# go-hooks

The hooks contract: the eleven-event vocabulary, the JSON a hook receives and
returns, the `allow` / `deny` / `ask` decision values, a per-hook failure mode
with no default, and pure managed > user > project layer resolution. Hosts
implement the engine; this module is only the contract, plus one helper that
runs a command-kind hook as a subprocess and a set of conformance fixtures.

A go-agentdef definition names hooks portably (`hooks: [guard-write-scope]`).
For that name to mean the same thing under Claude Code, Codex, Nanite or any
other host, they need to agree on event names, decisions and layering. That
agreement is this module.

## Install

```sh
go get github.com/hollis-labs/go-hooks
```

## Usage

`Resolve` merges the layers, `Validate` checks the resolved set, and
`cmdhook.Runner.Run` executes a command hook with the event's JSON on stdin.

```go
// Command hello resolves a hook, validates it and runs it as a subprocess.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	hooks "github.com/hollis-labs/go-hooks"
	"github.com/hollis-labs/go-hooks/cmdhook"
)

func main() {
	guard := hooks.Hook{
		Name:        "guard-write-scope",
		Event:       hooks.EventPreToolUse,
		Kind:        hooks.KindCommand,
		Matcher:     "Write",
		Command:     "/bin/sh",
		CommandArgs: []string{"-c", `cat >/dev/null; echo "writes outside the workspace are refused" >&2; exit 2`},
		Timeout:     5 * time.Second,
		OnError:     hooks.OnErrorClosed, // required: there is no default
	}

	// Managed beats user beats project, by hook name.
	resolved, err := hooks.Resolve([]hooks.Hook{guard}, nil, nil)
	if err != nil {
		log.Fatal(err)
	}
	h := resolved[0]
	if err := h.Validate(); err != nil {
		log.Fatal(err)
	}

	in := hooks.PreToolUseInput{
		CommonInput: hooks.CommonInput{Event: hooks.EventPreToolUse, SessionID: "s1", Cwd: "/work"},
		ToolName:    "Write",
		ToolInput:   map[string]any{"file_path": "/etc/passwd"},
	}
	if !hooks.MatchesTool(h.Matcher, in.ToolName) {
		return
	}
	out, err := cmdhook.Runner{}.Run(context.Background(), h, in)
	if err != nil {
		log.Fatal(err) // the caller applies h.OnError here
	}
	fmt.Println(out.Decision, "-", out.Reason)
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go).

## Packages

| Package | Role | I/O |
|---|---|---|
| `hooks` (root) | Events, `*Input` types, `Output`, `Decision`, `Hook`, `Resolve`, `MatchesTool`, `TruncateContext` | none |
| `hooks/cmdhook` | `Runner.Run`: run a `command` hook, decode stdout and exit code | subprocess |
| `hooks/conformance` | Fixture tree (`go:embed`) and a reference runner | test support |

The root package never imports `cmdhook` or `conformance`.

## Event catalog

Every `*Input` embeds `CommonInput` (`hook_event_name`, `session_id`, `cwd`,
optional `transcript_path` and `permission_mode`), so the encoded form is one
flat JSON object. The last column is what a host should do; `Output` does not
enforce it.

| Event | Extra input fields | Output fields honored | Injection point |
|---|---|---|---|
| SessionStart | `source` | `additionalContext`, `systemMessage` | First turn / first boot prompt |
| SessionEnd | `reason` | `systemMessage` (log only) | n/a, the session is over |
| UserPromptSubmit | `prompt` | `decision` (deny blocks), `additionalContext`, `reason` | Before the prompt reaches the model; context prepends to that turn |
| PreToolUse | `tool_name`, `tool_input`, `tool_use_id` | `decision`, `updatedInput`, `reason`, `additionalContext` | Before the tool runs; `updatedInput` replaces `tool_input` unless denied |
| PostToolUse | `tool_name`, `tool_input`, `tool_result`, `tool_use_id` | `additionalContext`, `systemMessage` | After the tool result, before the next model turn |
| PermissionRequest | `tool_name`, `tool_input`, `tool_use_id`, `reason` | `decision`, `reason` | The hook-side counterpart of an ask gate; `decision` drives the approval |
| PreCompact | `trigger` | `additionalContext`, `systemMessage` | Before compaction |
| PostCompact | `trigger` | `additionalContext`, `systemMessage` | After compaction, before the next turn |
| SubagentStart | `agent_id`, `agent_type` | `additionalContext`, `systemMessage` | The subagent's first turn |
| SubagentStop | `agent_id`, `agent_type`, `stop_hook_active`, `last_assistant_message` | `continue`, `stopReason`, `systemMessage` | Before the subagent's stop is finalized |
| Stop | `stop_hook_active`, `last_assistant_message` | `continue`, `stopReason`, `systemMessage` | Before the stop is finalized; `continue: false` with `stopReason` keeps it going |

`updatedInput` is honored for PreToolUse only; `Output` carries it on every
event purely so one decoder serves all eleven. The `SubagentStop` output
fields are inferred by parity with `Stop` and are not separately confirmed in
either vendor's documentation.

## Failure mode and exit codes

Every hook declares `OnError` (`open` or `closed`) and a `Timeout`. There is
no default: the zero `OnError` fails `Hook.Validate`. `cmdhook.Runner` never
reads `OnError`; it reports the outcome and the caller applies the mode.

| Outcome of a command hook | `Run` returns |
|---|---|
| exit 0, empty stdout | zero `Output`, no error |
| exit 0, JSON on stdout | decoded `Output`, no error |
| exit 0, invalid JSON or unknown decision word | error |
| exit 2 | `Output{Decision: deny, Reason: <stderr>}`, no error (an intentional block, `OnError` does not apply) |
| any other exit code | error wrapping the exit status and stderr |
| runs past `Hook.Timeout` | error wrapping `context.DeadlineExceeded`, even if the caller's context has no deadline |

## Layers

`Resolve(managed, user, project)` is whole-hook by name: the highest layer
that defines a name wins outright and lower definitions are discarded, not
merged field by field. It is pure and does not mutate its inputs. Validate the
resolved set, never a single layer.

## Conformance fixtures

`conformance/testdata/<Event>/<case>/` holds a real executable `hook.sh`, an
`input.json` and a `want.json` per case, covering all eleven events and, for
PreToolUse and PermissionRequest, the exit 0 allow, exit 0 deny, exit 2 block
and failure paths. A Go host calls `conformance.Load` and `conformance.Run`;
a host in another language re-implements the same loop over the same tree.

## Compatibility

This module is pre-1.0: minor releases may break the exported API, and there
is no compatibility promise yet. Pin an exact version, and read
[CHANGELOG.md](./CHANGELOG.md) before upgrading; every breaking change is
listed there. Native Claude Code and Codex payloads are not identical to this
contract (for example Claude nests some output under `hookSpecificOutput` and
uses the word `block`); normalizing them is the host adapter's job.

## Out of scope

- No engine: no event-firing loop, no per-session registry, no aggregation
  across multiple hooks on one event, no scheduling for `Async` hooks.
- No sandbox: no process confinement and no filesystem or network limits for
  command hooks. A script runs with the host's authority.
- No global `on_error` default, anywhere.
- No `http` kind. It is Claude-only and not part of the core.
- No config-file parsing: no `hooks.json`, `settings.json` or `config.toml`.
  `Resolve` takes already-parsed `[]Hook`.
- No `mcp_tool` dispatch: `MCPToolRef` is a name pair; calling the tool is the
  host's MCP client.
- No resolution of `skill:` or `host:` command prefixes.
- No adapters to Claude Code's or Codex's native JSON.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT, see [LICENSE](./LICENSE).
