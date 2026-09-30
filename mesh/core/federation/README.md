# go-federation

The cross-host hop-security layer for go-messaging traffic: mTLS-pinned peer identity, per-peer authority authorization and a configurable operation set, composing go-messaging's Router and httpstore.

Torque and Tether each hand-rolled federation over `go-messaging.Store`, with different trust models (Torque: pinned mutual TLS and an authority allowlist; Tether: plain HTTP and a self-asserted `?as=`). This module is the shared, hardened form of the first, as configuration.

## Status

Pre-1.0 and security-sensitive: the exported API may change in a minor release, and any change to what a peer may do is called out in the CHANGELOG. It has been built in isolation; neither Torque nor Tether uses it yet.

## Install

```sh
go get github.com/hollis-labs/go-federation
```

## Usage

```go
package main

import (
	"fmt"

	"github.com/hollis-labs/go-federation"
	"github.com/hollis-labs/go-messaging/memstore"
)

func main() {
	// With no federation.json, federation is off and nothing runs: no listener,
	// no foreign routes, no extra attack surface.
	f, err := federation.Enable("federation.json", memstore.New())
	fmt.Println(f == nil, err)

	// The shipped operation set is Torque's surface. Inbox and Subscribe are off
	// until an adopter widens the set on purpose.
	fmt.Println(federation.DefaultOpSet().Ops())
	wide, _ := federation.DefaultOpSet().Widen(federation.OpInbox)
	fmt.Println(wide.Allows(federation.OpInbox), federation.DefaultOpSet().Allows(federation.OpInbox))
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go).

## The model

- **Identity** is the client certificate: a peer is pinned by the SHA-256 fingerprint of its leaf (no CA), with several pins per peer for rotation, checked on every handshake including resumed ones. `MTLSPinnedResolver` is the only `IdentityResolver` shipped, and there is deliberately no resolver that trusts a caller's own claim of who it is.
- **Authorization** is by authority. A peer is registered for authorities; it may originate mail only from those, may have mail delivered only for authorities this install homes, and may touch only envelopes it is a party to. A thread shows it only its own envelopes, and `Consume` must name an address of the envelope.
- **The surface** is an `OpSet`. `DefaultOpSet` is Torque's reference: `Send`, `Get`, `Thread`, `Consume` and `Cancel` on, `Inbox` and `Subscribe` off. A disabled operation has no route and never reaches the store. Widening it is the adopter's deliberate act, and even then a peer may drain only a mailbox of an authority both installs hold.
- **Refusals reveal nothing**: the caller gets a fixed phrase; the reason goes to the audit log (`WithAuditor`), which records allowed calls too.
- **Composition**: `Dial` is an `httpstore` Store behind a pinned mutual-TLS client, and `Federation.Store` is a `go-messaging` `Router` with those Stores as foreign routes. Neither is reimplemented here.

`NewServer` serves the `OpSet` over the install's local Store (never the Router, so a request cannot be relayed back out); `Dial` reaches a peer; `LoadConfig` and `Enable` wire both from one JSON file in Torque's schema. No file means federation is off and nothing runs; a bad file is an error, and unknown keys are errors.

## Compatibility

This module is pre-1.0: minor releases may break the exported API. Pin an exact
version, and read [CHANGELOG.md](./CHANGELOG.md) before upgrading — every
breaking change is listed there.

## Out of scope

- A self-asserted identity resolver (Tether's `?as=`): ruled out for v1, and if one is ever added it must be named unmistakably as insecure.
- Certificate issuance, rotation tooling, revocation lists and any CA: peers are pinned by fingerprint.
- Retrying, queueing or store-and-forward of undeliverable messages, and migrating Torque or Tether onto this module.
- Message signing beyond the transport (the `fed.` metadata namespace is reserved for it and refused today).

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
