# go-hitl

A kind-agnostic contract for asking a human (or any slow responder) and getting back exactly one immutable outcome, with a JSON Schema bundle, Go wire types, a reference `Service` over a pluggable `Store`, an in-memory store, and a conformance kit.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/go-hitl
```

The module is not tagged yet, so this resolves only once a version exists. The root package `hitl` and `memstore` are stdlib-only; `schema` and `hitltest` depend on `github.com/santhosh-tekuri/jsonschema/v6`.

## Usage

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/hollis-labs/go-hitl"
	"github.com/hollis-labs/go-hitl/memstore"
)

func main() {
	ctx := context.Background()
	svc := hitl.NewService(memstore.New(), hitl.Options{})

	// A requester asks; the handle is all it keeps.
	handle, err := svc.Enqueue(ctx, hitl.EnqueueRequest{
		ContractVersion: hitl.ContractVersion,
		Kind:            "approval",
		IdempotencyKey:  "deploy:r17",
		Source:          hitl.SourceAssertion{ApplicationID: "ci", AgentID: "release-bot"},
	})
	if err != nil {
		log.Fatal(err)
	}

	// A human answers. The proof slot carries, but does not verify, a credential.
	_, err = svc.Respond(ctx, hitl.RespondCommand{
		ItemID:   handle.ItemID,
		Response: hitl.Response{Kind: "approval", Decision: "approved"},
		Participant: hitl.Participant{
			Responder: &hitl.Responder{Kind: "human", Ref: "operator@example.test"},
			Assurance: "authenticated",
			Proof: &hitl.Proof{Scheme: "webauthn", KeyRef: "cred:3f9a", Binds: []hitl.ProofBind{
				{Name: "approval_id", Value: handle.ItemID},
			}},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Anyone with the handle sees the one immutable outcome; a late withdraw loses.
	caller := hitl.CallerAssertion{ApplicationID: "ci"}
	got, err := svc.Get(ctx, hitl.GetCommand{ContractVersion: hitl.ContractVersion, ItemID: handle.ItemID, Caller: caller})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(got.Item.State, got.Item.TerminalOutcome.OutcomeState())

	_, err = svc.Withdraw(ctx, hitl.WithdrawCommand{ContractVersion: hitl.ContractVersion, ItemID: handle.ItemID, Caller: caller})
	fmt.Println(err != nil)
}
```

The same program lives in [`examples/quickstart`](./examples/quickstart/main.go).

## What is in the box

| Package | What |
|---|---|
| `hitl` (root) | Wire types (`State`, `CanTransition`, `Handle`, commands, `RetrievalResult`, the sealed `Outcome` union, typed errors with `DecodeError`/`EncodeError`), `Store`/`Record`/`CheckSwap`, and the reference `Service`. |
| `schema` | The embedded JSON Schema (draft 2020-12) bundle and `NewValidator(def)`. |
| `memstore` | In-memory `hitl.Store`. |
| `hitltest` | Fixtures, scenarios, `RunConformance` (wire-level `Adapter`), `RunStoreContract`, and `NewServiceAdapter`. |

The lifecycle: `Enqueue` returns a handle; `Get`, `Await` and `Withdraw` take it; a participant `Respond`s. Exactly one of five outcomes (`resolved`, `canceled`, `expired`, `failed`, `superseded`) ever happens; the loser of a race receives a conflict carrying the existing outcome. See [docs/CONTRACT.md](./docs/CONTRACT.md).

Two details worth knowing before you build on it:

- **Responder identity.** A `Participant` carries an open `responder{kind, ref}`, an open-string `assurance`, and an optional `proof{scheme, key_ref, binds}` for credentials such as a WebAuthn assertion. Core carries a proof; it does not verify one and does not rank it against `assurance`. Requiring a proof before honoring an outcome is caller policy.
- **Expiry.** `expires_at` is enforced by whoever owns the record. A respond or withdraw arriving at or after `expires_at` is refused atomically even if no sweeper has ever run. `Get` and `Await` never mutate: past `expires_at` they report the expired view computed on the fly and write nothing; only `Respond`, `Withdraw` and `ExpireDue` materialize expiry.

## Compatibility

The wire `contract_version` is `"1.0"` and is independent of the module version (and of Tangent's definition version, `"1.1"`). Within `1.0`, commands are strict (unknown members are rejected) and outputs are tolerant (unknown members are ignored). The module itself is unreleased and gives no compatibility promise; once tagged it will be pre-1.0, so minor versions may break the Go API. Go 1.26.6 or newer.

## Known limitations

- Unreleased and unadopted: no application uses this yet. The wire shapes were derived by reading Tangent's frozen `tangent.hitl-item` v1.0 contract; behavioral equivalence with Tangent's own service is **not** claimed (see [docs/CONFORMANCE.md](./docs/CONFORMANCE.md) for what was actually run).
- `Service` has no presentation layer: an enqueued item starts `presented` (revision 1) and is respondable at once. There is no `staged` progression, no FIFO or queue position.
- `Service` and `memstore` are in-process. `Await` wakes at once on changes made through the same `Service` and otherwise polls the store (250 ms by default); there is no cross-process notification.
- `Service` does not verify a `Proof`, does not check that an `assurance` string is meaningful, and applies no profile vocabulary unless you set `Options.ValidateResponse`.
- `memstore` keeps everything in memory; nothing is persisted. There is no SQL store yet.
- Who runs the expiry sweeper, and whether an unenforceable `expires_at` must be a validation error, are open questions the contract deliberately does not decide.
- Fixtures and scenarios are exercised against the reference `Service` only. No other implementation has been run through them.

## Out of scope

- Delivery and acknowledgement lifecycle, drafts, surfaces, queues and FIFO ordering, evidence blocks, retention.
- Quorum (`min_responders`), escalation, `subscribe` and `resume` verbs.
- Permit lifecycles, reusable or window-scoped grants, and break glass (a policy override, not HITL).
- The `allow`/`deny`/`ask` permission vocabulary. It is a separate vocabulary; `ask` is a policy pre-decision that triggers an interaction, never an outcome.
- Verifying a proof or running any verifier broker.
- The interaction-kind catalog and manifests.
- Default-on-timeout policy and who runs a sweeper: caller policy, not the contract.
- Importing or depending on go-envelopes, go-workflow or any application.

## Development

```sh
export GOWORK=off
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

Regenerating the bundle, fixtures and scenarios: `scripts/genbundle.py`, `scripts/genfixtures.py`, `scripts/genscenarios.py` (see the header of each). `make tangent-check` runs the opt-in cross-check against Tangent's schema file. CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
