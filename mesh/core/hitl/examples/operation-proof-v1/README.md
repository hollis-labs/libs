# Synthetic operation-proof vectors

Draft caller profile examples for CW-20261018-0082. They exercise a proposed
operation-bound WebAuthn evidence envelope carried by existing go-hitl response
extensions. They do not change the core contract or provide a production verifier.

`vectors.json` contains public P-256 verification material, canonical operation
and binding bytes, a challenge digest, and positive/negative assertion cases.
All identifiers and origins are synthetic. No private key is stored. Signature
regeneration is deliberately nondeterministic; consumers should use the checked-in
vectors for interoperability.

With Python 3 and `cryptography` installed:

```sh
python3 vectors.py               # check the published cases without creating keys
python3 vectors.py --generate    # replace cases with fresh synthetic signatures
node check-independent.cjs vectors.json  # independent ES256 and digest checks
GOWORK=off go run wirecheck.go vectors.json # go-hitl lifecycle and JSON carrier
```

Edit the generator, not the generated JSON. The checker is a fixture oracle with
explicit trusted-state inputs. It models authorization, proof consumption and
revocation decisions; it does not implement their storage, remote trust discovery,
credential registration, general CBOR/COSE parsing or HTTP admission. It supports
only the ASCII/string/bool subset used in these vectors, not general JCS. Production
adapters need strict bounded schema parsing and a complete WebAuthn library.

## Wire placement

- Enqueue extras: `operation` and its separate `op_digest`, plus `class=high`.
- Response extras: `assurance_evidence = {binding, assertion}`.
- Participant proof: `scheme`, `key_ref`, and bindings for operation digest,
  item ID, proof ID and audience.

The challenge is SHA-256 of `hollis operation proof v1`, a NUL byte, then the
canonical binding JSON. The binding includes the operation digest, item,
operation revision, audience, approving human, decision, credential reference,
fresh nonce, ceremony start/expiry and proof ID. The assertion signs the
authenticator data and SHA-256 of the original client-data bytes.

The carrier uses the existing `Response.Extra`; a proof descriptor alone contains
no assertion. Producers and browsers must not be allowed to set the trusted
participant, binding state or credential registry. A trusted ceremony adapter
constructs the response after checking the assertion. Consumers verify it again
against their current operation and authoritative outcome.

## What acceptance means

An `accept` result validates the modeled evidence at the provided clock and trust
state. It does not authorize issuance. A consuming application must independently
authorize the requester and atomically consume a continuation keyed by operation,
revision and proof ID. Repeated delivery returns the prior continuation result;
it must not create another credential or grant.

Freshness is conservative from ceremony start: `issued_at <= now < expires_at`,
with at most 300 seconds and no positive expiry leeway. `issued_at` is not a claim
that the authenticator provides a signed wall-clock authentication time. A real
authority must retain the matching ceremony and completion record.

Counter regression is checked at initial decision. At consumption, reverify the
same recorded assertion without treating its already-recorded counter as a new
login; current revocation and once-only continuation still apply. Authenticators
whose counters remain zero are represented by a positive vector.

## Reference standards

- [WebAuthn assertion verification](https://www.w3.org/TR/webauthn-2/#sctn-verifying-assertion)
- [JSON Canonicalization Scheme](https://www.rfc-editor.org/rfc/rfc8785.html)

These vectors are not a WebAuthn conformance suite. In particular, they do not
exercise extension data, authenticator backup flags, attestation or enrollment.
