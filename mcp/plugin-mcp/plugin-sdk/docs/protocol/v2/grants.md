# Capability grant DTOs

The stdlib-only `capability` package supplies the grant contract's transport data.
Protocol-2 `InitParams` uses the same GrantSet and RuntimeIdentity types.
Optional profiles require separate transport conformance before advertisement.

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
interpret it after a portable numeric check: integer-form tokens must be within
±9007199254740991 and every numeric value must be finite. Fractions and finite
exponent-form tokens remain descriptor-owned. Larger integers must be
string-encoded. Go retains raw JSON; TS parses ordinary numbers only after
checking their original tokens.

`GrantSet` marshals nil/empty as `[]`, refuses `null` on decode, and rejects
duplicate grant IDs. Different grants may share a capability name.
`ValidateForRuntime` checks every grant against a valid canonical tuple, even
when the set is empty. Decoding failures leave the receiver unchanged.

The reference [schema](../../../protocol/v2/grant.schema.json) covers object
shapes. Runtime checks additionally enforce duplicate-key rejection, decimal
integer tokens, timestamp ordering, tuple equality and unique grant IDs. JSON
inspection is bounded to 128 nesting levels. Shared raw token fixtures exercise
Go and TypeScript Grant/Init validators. Unpaired Unicode surrogates in keys
and values are rejected. These checks do not claim duplex Serve conformance. Historical protocol-v1 fixtures are unchanged.
