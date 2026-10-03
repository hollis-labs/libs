# agent-contracts-leaf

Zero-dependency agent contracts: Assignment, RunPolicy, InstanceStatus, and the capabilities and runtime vocabularies.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/substrate/llm-core/contracts
```

## Usage

```go
package main

import (
	"fmt"

	agentcontracts "github.com/hollis-labs/substrate/llm-core/contracts"
)

func main() {
	a := agentcontracts.Assignment{
		Agent: agentcontracts.AgentRef{Name: "incident-triage"},
		Run:   agentcontracts.RunPolicy{Lifetime: agentcontracts.LifetimeOneShot, Resume: agentcontracts.ResumeNever},
		Task:  agentcontracts.Task{Input: "triage alert 4412"},
	}
	if err := a.Validate(); err != nil {
		fmt.Println("invalid:", err)
		return
	}

	// The host that launches it records what it actually granted.
	rec := agentcontracts.LaunchRecord{
		Digests:        agentcontracts.Digests{Assignment: "sha256:..."},
		EffectiveTrust: agentcontracts.EffectiveTrust(agentcontracts.TrustTrusted, agentcontracts.TrustNormal),
	}
	fmt.Println(rec.Digests.Assignment, rec.EffectiveTrust)
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go).

## Compatibility

This module is pre-1.0 and makes no compatibility promise before its first tag:
a minor release may break the exported API, and while it has no external
consumers the rule is a clean break — no aliases, no deprecated shims. Pin an
exact version and read [CHANGELOG.md](./CHANGELOG.md) before upgrading.

## Out of scope

- The launch-profile schema and provider/runtime configuration — those stay host-side. Package `runtimes` names the runtimes, modes and runtime capabilities; which runtime supports what is the go-providers descriptor registry's.
- Permission decisions: `Grants.Permissions` is an opaque list of rule tokens that go-permission interprets.
- The go-materialize manifest: a host assembling a full persisted launch record attaches it around `LaunchRecord`.
- The agent definition file format, its parser and digest (go-agentdef).
- Hooks and tool selection.
- Anything that launches, enforces or persists: this module is pure data.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
