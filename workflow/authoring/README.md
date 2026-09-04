# Workflow Authoring Front Ends

`authoring` is a value-style view over the canonical `graph.Graph`
contract. It does not execute plans or define a second workflow language.

Go callers build an immutable graph and then use the ordinary compiler,
dependency inference, validation, definition-resolution, and policy catalogs:

```go
built := authoring.New("release", "v1").
    Authority("project").
    Node(graph.Node{
        ID: "publish", Kind: "http", KindVersion: "v1",
        Effects: graph.EffectSet{graph.EffectMutate},
    })

result := built.Compile(ctx, authoring.CompileOptions{
    Validation: compile.ValidationOptions{StepKinds: kinds},
})
```

Every builder method returns independently owned graph data. `Compile` never
creates mutable in-flight execution state.

## Generated clients

The committed graph schema is the authority for graph DTOs and schema
identifiers. A host may use it to generate its transport-specific clients and
authoring-envelope preflight. Generated clients should provide equivalents of:

- `createGraphAuthoringEnvelope` and `createWorkflowSourceAuthoringEnvelope`;
- `decodeAuthoringEnvelope`, with strict unknown-field, byte, depth, node, edge,
  and exact schema/version checks;
- a host-specific client generated from the host's operation map.

Regenerate and verify the engine schema from the repository root:

```sh
go generate ./graph
go test ./graph/...
```

A consuming host is responsible for a byte-for-byte stale-artifact gate for any
client it generates.

## Agent ingress

A host agent-ingress service should accept a bounded raw `authoring.Envelope`.
It should stage exact material only while its definition resolver validates it,
then run contract tests before registry publication. Namespace and definition
authorization, step-kind and effects validation, policy hooks, provenance, and
exact digests remain mandatory host checks. A request without a contract suite
must not mutate the catalog.

Schema identifiers and versions are exact, not negotiated by content sniffing.
Legacy registry records with all discriminator fields absent default only to
the historical graph-native workflow source contract. A qualified registry
name is always `<namespace>/<source-local graph ID>`; definition resolution
removes that single authority namespace before comparing `Definition.ID` and
`Graph.ID`.

`SemanticPlanFingerprint` and `SemanticPlanDocument` exist only for
cross-front-end conformance. Runtime `ExecutionPlan.Digest` and definition
digests remain bound to exact source identity.
