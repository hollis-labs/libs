# go-reflexes

A database-agnostic sense, integrate, act steering engine: Nanite's reflex
engine as a standalone library. A reflex is a JSON trigger (predicate, event
or interval) over a windowed `State` plus an action of a named kind. The
engine evaluates triggers, arbitrates the fired reflexes per action kind
(`deny_overrides`, `first_applicable` or `all_applicable`) under a system,
kind, reflex cooldown cascade, applies actions through a registry of
handlers, and emits one trace record per firing. The standard library is the
only dependency, and nothing here imports an application.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

Every adopter other than Nanite is inferred, not established. Nanite does not
consume this module yet.

Behavioral equivalence with Nanite's engine is not established. The
`TestRunEquivalence_*` goldens were transcribed from Nanite's code, not
captured from a Nanite run, and they pin this library's own trace shape
(including host-defined `attrs` and top-level live classification fields).
Nanite's adoption should confirm equivalence against
a real Nanite trace.

## Install

```sh
go get github.com/hollis-labs/go-reflexes
```

## Usage

The host supplies a `Source` (candidate reflexes) and a `KindCatalog` (action
kinds and their combining algorithms); both are tiny consumer-defined
interfaces, faked here in memory. `Engine.Run` is the whole pipeline.

```go
package main

import (
	"context"
	"fmt"
	"log"

	reflexes "github.com/hollis-labs/go-reflexes"
)

// memory is a fake Source and KindCatalog: a real host reads these from its
// own store.
type memory struct{ rows []reflexes.Reflex }

func (m memory) Candidates(context.Context, string, string) ([]reflexes.Reflex, error) {
	return m.rows, nil
}

func (memory) ActionKinds(context.Context) ([]reflexes.ActionKind, error) {
	return []reflexes.ActionKind{{Name: "inject_reminder", Category: "system_message", CombiningAlgorithm: "all_applicable"}}, nil
}

func main() {
	src := memory{rows: []reflexes.Reflex{{
		ID:          "r1",
		Name:        "wake-on-mail",
		TriggerKind: "event",
		TriggerSpec: `{"name":"mail_received"}`,
		ActionKind:  "inject_reminder",
		ActionSpec:  `{"body":"You have unread mail."}`,
	}}}
	engine, err := reflexes.New(src, src)
	if err != nil {
		log.Fatal(err)
	}
	res, err := engine.Run(context.Background(), reflexes.RunInput{
		AgentID: "a1",
		State:   &reflexes.State{SessionID: "s1", MailUnreadCount: 2},
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, a := range res.Applied.Actions {
		fmt.Println(a.ReflexName, a.ActionKind, a.Spec["body"])
	}
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go).

### Seams

| Seam | Purpose | Required |
|---|---|---|
| `Source` | `Candidates(ctx, agentID, class)`: the reflexes to evaluate | yes |
| `KindCatalog` | `ActionKinds(ctx)`: combining algorithm, category, default cooldown per kind | yes |
| `StateSource` | `Collect(...)`: builds a `State` when `RunInput.State` is nil. Not implemented here | no |
| `TraceStore` | `LogEvent` and `BumpAgentReflexFired`: one event and one fired-count bump per firing | no |
| `Filters` | filter the `State` and each fired action; observe `Fired` and `Staged` | no |

### Acting: handlers and staged kinds

The act side is a registry, not a fixed switch. `Executor.Handle(kind, h,
WithPhase(p))` registers an `ActionHandler`:

- `PhaseResolve` (default) runs inside `Resolve`, before the trace write. A
  handler error is logged and swallowed; the action still fires.
- `PhaseAfterEmit` runs after the trace write. A handler error does not undo
  the firing (it is already counted); `Run` returns the first error after
  attempting every fired action.

`Executor.Stage(kinds...)` marks kinds whose effect is a decision the caller
acts on (returned in `Result.Applied`). Built in: `inject_reminder`,
`force_tool_choice` and `dispatch_to_agent` are staged; `halt_session`,
`add_schedule` and `send_message` are staged until a handler is registered;
any other unregistered kind, including `resume_loop_run`, is an
`unknown action_kind` error. `send_message` has no handler and is documented
inert.

The one kind that needs a real handler is `resume_loop_run`. Register it at
your composition root as a closure over your loop runtime; the library needs
no `Resumer` interface (see `ExampleExecutor_Handle`):

```go
engine.Executor().Handle("resume_loop_run", reflexes.HandlerFunc(func(ctx context.Context, f reflexes.Firing) error {
	id, _ := f.Spec["loop_run_id"].(string)
	return loops.ResumeLoopRun(ctx, id)
}), reflexes.WithPhase(reflexes.PhaseAfterEmit))
```

### Signals: 0 means unknown

Numeric message signals (`InputTokens`, `OutputTokens`, `CacheRead`,
`ToolCalls`) and `PrefixTokens` are plain ints where 0 means unknown or none.
A host that does not report cache reads makes `cache_read_window = 0` true.
Write predicates as conjunctions over several signals. Explicit "known" flags
may come in a later version; the semantics do not change in v0.1.

### Live classification and host attributes

`State.ScopeTier` and `State.ExecutionPattern` carry the current turn's
classification, supplied by the host. Predicate kinds `scope_tier` and
`execution_pattern` compare those fields directly:

```json
{"kind":"AND","clauses":[{"kind":"scope_tier","value":"open"},{"kind":"execution_pattern","value":"subagent"}]}
```

Comparisons are case-sensitive. `op` accepts `=` (the default), `==` and
`!=`; any other operator returns false without an error. A missing or
non-string `value` is an empty string, which matches an unset signal under
`=`. There is no message window or cold-start guard.

Firing traces copy these signals verbatim into top-level `scope_tier` and
`execution_pattern` fields, omitting empty strings. The library does not
insert them into `attrs`. `State.Attrs`, the generic `attr` predicate and the
trace's copy of `Attrs` remain available for host-defined signals.

When upgrading from v0.1.0, hosts that mapped classification through `Attrs`
can populate the first-class fields and use the corresponding predicate kinds.
This changes their trace shape from nested `attrs` entries to top-level fields;
update trace readers along with the state mapping. Explicit entries still
supplied in `Attrs` retain their existing meaning and placement.

### `Filters`, not `Hooks`

The plugin seam (filters that rewrite the `State` and each fired action,
observers told about each firing) is the `Filters` interface and the
`WithFilters` option. It descends from Nanite's `PluginHooks`. It is
deliberately not called `Hooks`: the sibling module
`github.com/hollis-labs/go-hooks` owns the `Hook`/`hooks` vocabulary (a
reflex is conceptually one kind of hook implementation), and this library's
filter seam must not collide with it. This module does not import go-hooks.
The name was chosen before the first tag.

## Compatibility

This module is pre-1.0: minor releases may break the exported API. Pin an exact
version, and read [CHANGELOG.md](./CHANGELOG.md) before upgrading, since every
breaking change is listed there. Field names and JSON tags of `Reflex`,
`ActionKind`, `State`, `MessageSignal`, `EventSignal`, `AppliedAction` and
`CandidateOutcome`, and the trace record shape, follow the source system's on
purpose so a host can alias its own types to them; changing them is a
breaking change.

## Out of scope

- Any database, SQL, or persistence: the host implements `Source`,
  `KindCatalog` and `TraceStore`.
- Building a `State` from a store or from a child harness's events (Nanite's
  `StateCollector` stays in Nanite; `StateSource` is an open seam only).
- Seed reflexes and product policy, opt-out rules, and the SQL that lists
  candidates.
- Validation or a JSON Schema for `trigger_spec` and `action_spec`
  (a separate spec effort).
- Reminders, reactions, loop detection, and a scheduler: `add_schedule` is a
  handler you register; nothing here shares code with go-scheduler.
- Wiring `send_message` or `dispatch_to_agent`: the latter is a decision
  output the caller acts on.
- Ingesting hook callbacks or any child-CLI input, and reflexes for
  externally launched agents.
- Unknown-versus-zero numeric signals beyond the documented 0 = unknown.
- Adoption by any application, including Nanite.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
