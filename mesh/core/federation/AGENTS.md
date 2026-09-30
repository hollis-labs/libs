# go-federation

The cross-host hop-security layer for go-messaging traffic: mTLS-pinned peer identity, per-peer authority authorization and a configurable operation set, composing go-messaging's Router and httpstore.

It is not: a messaging store, a router, an HTTP client for messaging, or a place for a weaker trust model. go-messaging owns the Store contract and its `Router`, `httpstore` owns the HTTP client, and both are composed here, never rebuilt. The mistake this repo attracts is making federation easier by trusting more (an identity the caller asserts, an operation on by default, a helpful error message); every one of those is a security regression, not a convenience.

## Start Here

- `doc.go` — the trust model in one page. `ops.go` (`OpSet`, `DefaultOpSet`), `identity.go` (`IdentityResolver`, `MTLSPinnedResolver`), `peers.go`, `fingerprint.go`, `tlsconfig.go` (the pin check), `authz.go` (the authorizer), `server.go` (the surface), `dial.go` (the outbound Store), `config.go` and `federation.go` (the file, the Router composition).
- `testsupport_test.go` builds real mutual-TLS fixtures; `federation_test.go` runs whole installs through the config path.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either. Dependencies are go-messaging (root and `httpstore`) and the standard library.
- **No self-asserted identity resolver.** `MTLSPinnedResolver` is the only `IdentityResolver`; do not add one that believes a caller's claim, and do not leave a stub for it. (Ratified 2026-09-30, `federation.security_model.opset_and_resolver`.)
- **`DefaultOpSet` is Torque's surface: `Inbox` and `Subscribe` off** (`TestDefaultOpSetIsTorquesSurface`, `TestDefaultOpSetDoesNotMountInboxOrSubscribe`). A disabled operation has no route and the store is never called; widening is the adopter's act.
- **The pin is checked in `VerifyConnection`, never `VerifyPeerCertificate`** (`TestTLSConfigsUseVerifyConnectionOnly`): Go skips the latter on resumed sessions, so a removed pin would keep working through a ticket. The resolver re-checks validity and the pin itself (`TestMTLSPinnedResolverContract`) and does not rely on the TLS config having.
- **The server wraps the local Store, never a Router**, so a request cannot be re-dispatched out (the relay the design forbids). The authorizer checks the routing invariant (this install homes the governing authority) and the trust boundary (the caller is authoritative for a party) on every call (`TestAuthorizeSend`, `TestAuthorizeEnvelopeAccess`, `TestTheHopEnforcesTheTrustBoundaryBetweenInstalls`).
- **A refusal reveals nothing.** The caller gets a fixed phrase; store error text, panic values and authorization detail go only to the audit record (`TestErrorsAndPanicsNeverLeakInternals`, `TestSendRefusals`). A thread answers the same whether or not the id exists (`TestThreadIsFilteredToTheCallersEnvelopes`).
- **Config fails loudly.** Absent file: `(nil, nil)`, federation off. Present and wrong: an error; unknown keys, an unpinned route, an empty `ops` and an unknown operation are all errors (`TestLoadConfigRejectsInvalidFilesLoudly`).
- **Where this differs from Torque's `internal/federation`, on purpose** (CHANGELOG lists it): TLS 1.3 minimum; `VerifyConnection`; a Thread is filtered to the caller's envelopes instead of answering 403; `Consume` must name an address of the envelope; `fed.` metadata is refused, not ignored; error and panic text is not echoed; `NewServer` returns an error; audit goes to a callback, not `log.Printf`; bodies and list limits are bounded; redirects are not followed.
