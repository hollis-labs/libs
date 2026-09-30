// Package federation is the hop-security layer for go-messaging traffic between
// installs: the seam a message crosses when its recipient's authority is homed on
// another host.
//
// go-messaging's Router already decides where a message goes (by the authority of
// its address) and httpstore already knows how to talk to a remote Store. What
// neither does is defend the hop: who is calling, which authorities they may act
// for, and which operations cross at all. That is this package, and it composes
// both rather than rebuilding either: Dial is an httpstore Store behind a pinned
// mutual-TLS client, Federation.Store is a Router with those Stores registered as
// foreign routes.
//
// # The trust model
//
//   - Identity is the client certificate. Peers are pinned by the SHA-256
//     fingerprint of their leaf certificate (no CA, no revocation lists), a peer may
//     pin several fingerprints so a certificate can be rotated without downtime, and
//     the check runs on every handshake, resumed ones included. MTLSPinnedResolver
//     is the only IdentityResolver this module ships; it deliberately ships none
//     that trusts an identity the caller merely asserts. If one is ever added it
//     must be named as insecure and be a later, explicit opt-in.
//   - Authorization is by authority. A peer is registered for a set of authorities.
//     It may originate mail only from those (no impersonation), may have mail
//     delivered only for authorities this install homes (no relay), and may touch
//     only envelopes it is a party to. A thread shows a peer only its own
//     envelopes. Consume must name an address of the envelope.
//   - The surface is a configurable OpSet. DefaultOpSet is Torque's reference:
//     Send, Get, Thread, Consume and Cancel on, Inbox and Subscribe off. A disabled
//     operation is not mounted and its store is never called. Widening the set is a
//     deliberate act by the adopter, and even then a peer may drain only a mailbox
//     of an authority both installs hold.
//   - Nothing about a refusal is sent to the caller beyond a fixed phrase. Why goes
//     to the audit log (WithAuditor), which also records every allowed call.
//
// # Shape
//
// NewServer serves an OpSet over the install's local Store (never the Router, so a
// request cannot be relayed back out). Dial reaches a peer. Config, LoadConfig and
// Enable wire both from one JSON file in Torque's schema: no file means federation
// is off and nothing runs; a bad file is an error, never a silent default.
package federation
