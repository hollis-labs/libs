# go-permission

Tool-invocation authorization core: allow/deny/ask decisions, rules, approvals and path grants.

An `Engine` answers "may this session run this tool with this input?" from a
mode, an ordered rule set and per-session grants, and drives the approval round
trip when the answer is *ask*. It is in-process, has no transport and one
non-stdlib dependency (`gopkg.in/yaml.v3`).

| Package | What it is |
|---|---|
| `github.com/hollis-labs/substrate/harness/interception/permission` | `Engine`, `RuleSet`/`Rule`, approvals, subagent rule derivation, `Auditor` |
| `.../pathgrants` | session-scoped path grants from explicit path mentions, with parent-session lineage |
| `.../summary` | renders effective path access as prompt text |

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/substrate/harness/interception/permission
```

Requires Go 1.26.6 or newer.

## Usage

```go
package main

import (
	"context"
	"fmt"
	"time"

	permission "github.com/hollis-labs/substrate/harness/interception/permission"
)

func main() {
	rules := &permission.RuleSet{Rules: []permission.Rule{
		{Tool: "shell", Pattern: "rm -rf", Behavior: permission.DecisionDeny},
	}}
	e := permission.NewEngine(permission.ModeDefault, rules,
		permission.WithApprovalTimeout(5*time.Second))
	ctx := context.Background()
	meta := permission.ToolMeta{IsDestructive: true}

	// A rule denies this outright.
	res := e.Check(ctx, "session-1", "shell", map[string]any{"command": "rm -rf /tmp/x"}, meta)
	fmt.Println(res.Decision) // deny

	// No rule matches, and the tool is destructive: the mode default asks.
	res = e.Check(ctx, "session-1", "shell", map[string]any{"command": "make clean"}, meta)
	fmt.Println(res.Decision) // ask

	// Surface the question over your own transport, then wait for the answer.
	req := e.RequestApproval("session-1", "shell", map[string]any{"command": "make clean"}, res.Reason)
	go e.Respond(req.ID, permission.DecisionAllow, permission.ScopeSession, "session-1")
	resp := e.WaitForApproval(ctx, req)
	fmt.Println(resp.Decision, resp.Scope) // allow session

	// "Allow for session" is now remembered for this tool in this session.
	res = e.Check(ctx, "session-1", "shell", map[string]any{"command": "make clean"}, meta)
	fmt.Println(res.Decision, "-", res.Reason) // allow - session grant
}
```

Rules load from YAML with `LoadRulesFromFile` (which calls `Validate`) and merge
with `MergeRuleSets`. Call `RuleSet.Validate` on any rule set you build in
code: a misspelled `behavior` is otherwise silently ignored.

A host that wants an audit trail passes `WithAuditor`; `SlogAuditor` logs the
approval lifecycle through `log/slog`, and events never carry tool input.
`ScopeProject` approvals go to a `RuleStore` you supply with `WithRuleStore`
(none ships; without one they behave as `ScopeOnce`).

### Precedence

`Engine.Check` applies, first match wins:

    yolo > plan > session grant > deny rules > ask rules > allow rules > mode default

Two consequences surprise people and are kept on purpose: `ModeYolo` beats deny
rules, and session grants are keyed by tool **name** only, so "allow for
session" on a tool also overrides later deny rules for that tool (for any
input). `TestPrecedence_*` pins the order.

### Sharp edges

- **A rule `Pattern` only sees recognized input keys.** Path patterns are
  matched against the input keys `path`, `file`, `directory` and `file_path`;
  command patterns against `command`. A tool whose input uses another key
  (`paths`, `url`, `cmd`) never matches a rule that has a `Pattern`, so a deny
  rule with a `Pattern` silently does not apply to it. Widen the keys with
  `WithMatcher(permission.Matcher{PathKeys: ...})`; nothing is widened for you.
- **Path matching is lexical.** A path from tool input is cleaned
  (`filepath.Clean`) before matching, so `..` cannot step around a rule, but
  symlinks are not resolved: a link is matched by the name it is given.
- **Command matching is advisory.** By default a command pattern is a plain
  substring test, so an allow pattern `git` also matches `git; rm -rf ~`. Do not
  treat it as a security boundary; supply `Matcher.Command` (for example a
  tokenising matcher) if you need more, and use a sandbox for real confinement.

## Compatibility

This module is pre-1.0: minor releases may break the exported API. Pin an exact
version, and read [CHANGELOG.md](./CHANGELOG.md) before upgrading — every
breaking change is listed there. It needs Go 1.26.6 or newer (the `go` line of
`go.mod`).

## Out of scope

- Sandboxing or process confinement. A permission verdict is not isolation.
- Command denylists and shell parsing. Command matching is an advisory
  substring hook, not a policy engine.
- Transports and UI. There is no HTTP, SSE or prompt; the host carries
  `ApprovalRequest`s to whoever decides and calls `Respond`.
- Tool-grant membership: which tools an agent is offered at all. That is a
  separate concern from allow/deny/ask on a call.
- Advisory or rewriting policies (observe, nudge, rewrite, block). This module
  answers allow, deny or ask only.
- Persisting project-scope rules. `RuleStore` is the seam; there is no file-backed
  implementation.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
