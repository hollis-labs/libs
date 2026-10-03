# Capability grant DTOs

The additive `capability` package supplies the grant contract's transport data.
It does not change the current subprocess `InitParams`, `ProtocolVersion` or
`Serve`. Protocol-2 handshake and optional profiles require separate runtime
implementation and conformance before advertisement.

`Grant` has required identifiers, descriptor version, opaque normalized scope,
the host-issued tuple `(host_instance, owner_id, owner_generation)`, audience,
UTC issue/expiry timestamps and policy revision. `RuntimeIdentity` represents
that tuple independently of grants; it is unrelated to a manifest's execution
engine. Hosts own policy, current expiry, authentication and descriptor scope
validation. A structurally valid grant never proves execution permission.

`json.Marshal` and `json.Unmarshal` validate these types. Decoders reject
missing/null fields, unknown or incorrectly cased fields, duplicate keys
(including nested opaque JSON), invalid integer tokens/ranges and timestamps.
Generation is a positive integer at most 9007199254740991; descriptor versions
are positive uint32 integers. Integer tokens cannot use fractions or exponents.
Timestamps use UTC RFC3339 with `Z`, optional 1–9 fractional digits, and expiry
strictly after issue. Opaque scope remains raw, non-null JSON: descriptor owners
interpret it, and large numbers are preserved without float conversion.

`GrantSet` marshals nil/empty as `[]`, refuses `null` on decode, and rejects
duplicate grant IDs. Different grants may share a capability name.
`ValidateForRuntime` checks every grant against a valid canonical tuple, even
when the set is empty. Decoding failures leave the receiver unchanged.

The reference [schema](../../../protocol/v2/grant.schema.json) covers object
shapes. Runtime checks additionally enforce duplicate-key rejection, decimal
integer tokens, timestamp ordering, tuple equality and unique grant IDs. JSON
inspection is bounded to 128 nesting levels. Shared raw token fixtures exercise
Go and the reusable TypeScript scanner; this is not TS Grant or duplex Serve
conformance. Historical protocol-v1 fixtures are unchanged.
