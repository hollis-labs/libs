# Runtime references

Experimental, standard-library-only values shared by gate, harness and SDK
consumers. These values describe host-issued facts; constructors, JSON and
`Validate` never issue identity, grant permission, provision resources or restore
files.

`Binding` separates operative/seat, conversation/owner and attempt/generation.
The complete binding compares exactly; a provider-native session ID does not
replace any component. One operative can own several separate conversations.
`Generation` is positive. Producer sequence/restart generation, PTY byte offset
and an opaque committed replay cursor are distinct types.

`Reference` pins ID, revision, digest, issuer and owner. Digests use scheme
`sha256` and 64 lowercase hex characters. Operation consumers additionally require
a canonical descriptor version. References are locators, never credentials.

`EnvironmentRef` pins an existing receipt, workspace, lease, grants, unique
capability IDs and absolute expiry. `ValidateAt` checks declared capabilities and
the caller-supplied time only. The trusted host must resolve the receipt and verify
current issuance, owner, grants and lease before any effect.

`RecoveryLocator` carries an explicit source binding and pinned transcript,
workspace, optional plan/snapshot/pending-operation references and committed cursor.
All portable references must belong to the conversation owner, and workspace must
match the environment. The fake recovery consumer verifies the entire locator
against separate host records and an explicitly issued successor binding. There
is no implicit newest-session selection or transfer of hidden provider context.
Snapshot scope/coverage, restoration, transcript access, input-writer leasing and
recovery reconciliation remain host/controller responsibilities.
