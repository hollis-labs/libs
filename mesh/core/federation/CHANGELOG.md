# Changelog

All notable changes to go-federation are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- The module: the hop-security layer for go-messaging traffic between installs, extracted from Torque's `internal/federation` (the reference) and generalized into configuration. It composes `go-messaging`'s `Router` and `httpstore` and reimplements neither.
- `MTLSPinnedResolver`, the only `IdentityResolver` shipped: callers are pinned by the SHA-256 fingerprint of their leaf certificate (several per peer, for rotation), with `PeerRegistry`, `Fingerprint`, `NormalizeFingerprint` and `ValidateFingerprint`, and the TLS configs (`ServerTLSConfig`, `ClientTLSConfig`, `ClientHTTPClient`).
- `OpSet` and `DefaultOpSet`: the operations exposed across the hop. Ratified 2026-09-30 (`federation.security_model.opset_and_resolver`): the default is Torque's surface, `Send`, `Get`, `Thread`, `Consume` and `Cancel` on, `Inbox` and `Subscribe` off. A disabled operation is not mounted.
- `Server` (`NewServer`, `Handler`, `Serve`, `Run`): the operations of an `OpSet` over the local Store, with an authorizer generalizing Torque's routing-invariant and trust-boundary checks, `WithAuditor`, `WithMaxPayloadBytes` and `WithMaxListLimit`. `Inbox` and `Subscribe` are implemented for the case an adopter widens the set: a peer may drain only a mailbox of an authority both installs hold.
- `Dial`: a `go-messaging` Store reaching a peer over pinned mutual TLS, through `httpstore` (Torque's federation profile, with `Inbox`/`Subscribe` carried only if the `OpSet` turns them on).
- `Config`, `LoadConfig`, `Enable` and `Federation`: Torque's config schema plus an optional `ops` list. No file means federation is off; a bad file is an error. `Federation.Store` is a Router with each foreign route dialed; an install homing several authorities gets a guard that refuses unknown ones with `ErrNoRoute`.
- It passes `messagingtest.RunContract` through a real mutual-TLS hop: without `Inbox`/`Subscribe` under `DefaultOpSet`, and whole under a widened set.

### Fixed

- A Thread applies the party filter before the list limit: other parties' envelopes ahead of the caller's in a thread no longer starve it. The store is asked for progressively more (it has no offset), bounded at ten times the server's list limit per request; the response shape is unchanged.
- The reserved `fed.` metadata prefix is refused case-insensitively, after trimming surrounding whitespace, and any metadata key holding a control or format character (zero-width, bidi) is refused, with the same 422 as before.
- Peer-controlled text in an `AuditRecord` (addresses, ids, reasons) has control and format characters replaced with U+FFFD, so a peer cannot forge a log line.
- `NewServer` returns an error for `WithAuditor(nil)`, `WithClock(nil)`, a nil `ServerOption` and a typed-nil Store or `PeerRegistry`, instead of panicking at request time.
- `Serve` no longer leaks its shutdown goroutine when the listener fails, and waits for the drain to finish before it returns.

### Changed

- `Consume` additionally requires the caller to be authoritative for the recipient's authority: a peer holding only the sender side could previously mark the local recipient's copy consumed. It is refused with the same 403 and body as consuming for an unrelated recipient.
- `Send` with a non-empty `in_reply_to` or `thread_id` must name an exchange the caller is a party to: the reply target must exist and the caller be a party to it, and a thread that holds envelopes must hold at least one of the caller's. A missing and a foreign target are refused identically (403, fixed body, before any store write); the audit record keeps the reason. A fresh, unused thread id is allowed.

### Decisions

- No self-asserted identity resolver in v1 (the ratified default): Tether's `?as=` pattern is not carried over, and no stub is left for it. If one is ever added it must be named unmistakably as insecure and be a later, explicit opt-in.
- Config schema: Torque's as the baseline (builder-ratified), plus `ops`.

### Differences from Torque's `internal/federation`

Deliberate hardening; each has a test.

- TLS 1.3 is the minimum (Torque: 1.2), and the pin is checked in `VerifyConnection`, which Go runs on resumed sessions too, rather than `VerifyPeerCertificate`, which it skips.
- `Get`, `Consume` and `Cancel` answer 404 with the same body for an envelope the caller is not a party to as for a missing id (Torque: 403 versus 404, which tells a peer whether an id exists); the audit record keeps the difference.
- A Thread is filtered to the envelopes the caller is a party to and answers 200 for a thread the caller has no part in, rather than 403 for a non-party: the response does not say whether a thread id exists, and a thread holding other parties' mail no longer shows it.
- `Consume` requires the recipient to be an address of the envelope: a party could otherwise mark an envelope consumed for an unrelated recipient.
- Consume requires the caller be authoritative for the recipient's authority.
- `Send` may reply to, or join the thread of, only an exchange the sender is a party to.
- Envelope metadata under the reserved `fed.` prefix is refused (Torque ignored it), so no peer occupies the namespace before signed envelopes exist.
- The caller is never sent authorization detail, store error text or a panic value; these go to the audit record.
- `NewServer` returns an error for every misconfiguration; a nil `OpSet` means `DefaultOpSet` and an `OpSet` with nothing on is an error. Audit is a callback (`WithAuditor`, `SlogAuditor`), not `log.Printf`.
- Request bodies and list results are bounded, the outbound client does not follow redirects, and `Serve` refuses a TLS config that does not require client certificates.
