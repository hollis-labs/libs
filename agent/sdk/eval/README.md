# Deterministic SDK scenarios

This experimental package runs the actual SDK loop using only synthetic model,
tool and authority adapters. Its public `Options` contains data only; there is
no path for injecting live models, tools, services, endpoints or admission ports.
The existing fake host reuses the real HITL service with an in-memory store.

```go
scenario, err := eval.Decode(reader)
if err != nil {
    return err
}
result, err := eval.Run(ctx, scenario, eval.Options{})
```

`Decode` accepts one YAML/JSON document, capped at 1 MiB, and refuses unknown
fields. `Run` also validates programmatically constructed scenarios before loop
activity, detaches caller-owned data, and returns a JSON-serializable `CaseResult`
with rubric findings, actual SDK outcome, per-model accounting and an immutable
trace recording. Validation errors return before creating execution evidence.
Rubric failure returns `Passed=false` with no execution error; actual SDK failure
also returns its error and the observed terminal/effect evidence.

From the SDK module directory:

```sh
go run ./examples/eval ./eval/testdata/pass.yaml
go run ./examples/eval ./eval/testdata/deliberate-fail.yaml
```

The first exits successfully after two model turns and one gated fake tool call.
The second deliberately exits unsuccessfully: prose claiming a tool call cannot
satisfy the `called_tool` criterion.

## Scenario contract

The top-level fields retain Nanite's scenario shape:

- `id`, `benchmark`, `description`, `user_message`, `expected_behavior`.
- `tools_offered`, now with full `input_schema` and a synthetic `mock`.
- `rubric`, retaining deterministic text and tool criteria.
- `prev_turns`, recognized but explicitly unsupported when nonempty.

`model_turns` supplies structured text/tool calls, optional presence-aware usage,
a stored price snapshot and virtual `duration_ms`. Each tool mock has a content
string and an explicit `outcome` of `succeeded` or `unknown`. Tool input schemas
and all scripted arguments are validated before execution. Remote schema
references are refused with no loader/network access. Scripted JSON numeric tokens are retained through SDK argument binding. Schema
validation uses a numeric view for the published validator: plain 64-bit integers
remain exact; wider integral values that cannot be validated without rounding
are refused.

The next actual model
request must contain the SDK's correctly paired tool results.

The supported rubric checks `called_tool`, `avoided_tools`, exact `tool_order`,
`contains_any`, `does_not_contain`, and `expected_status` (default `succeeded`).
Tool criteria use host-reported execution-start receipts from actual loop calls;
a tool name in response prose, an offered schema, model request, approval or
native delivery is insufficient. Text criteria are case-insensitive. A runtime
failure cannot become a passing case merely by expecting a failed status.

`asked_clarification`, even when false, is explicitly unsupported. There is no
heuristic clarification classifier, inferred conversation history, provider
resume or implicit rich grader. `expected_behavior` is descriptive, not an
unimplemented judge. Unsupported mock outcomes, malformed schemas/arguments,
unknown fields and impossible accounting fail before effects.

A scenario is capped at 64 model turns, 128 offered tools, 128 scripted tool
calls total, 32 calls per model turn, and 64 KiB per mock result. Each virtual
turn duration is between zero and 60,000 ms. Calls and grading follow authored
list order. A script which is not fully consumed is a failed case.

## Accounting and determinism

`usage` carries optional `input`, `output`, `cache_read`, `cache_write` and
`reasoning` counts. Missing means unknown; an explicit zero means measured.
`input_includes_cache` and `output_includes_reasoning` explicitly request the
provider-style inclusive-to-disjoint conversion used by the shared ledger.
Missing subdivisions remain unknown and derived remainder counts are estimated.
Impossible negative/excessive counts, non-finite derived costs or subdivisions
exceeding a total are
refused; no negative or double-counted ledger is produced. Counts are bounded
at one billion per component for this synthetic fixture format.

Each actual synthetic model call explicitly reports one final `usageledger.Row`
to the recorder. The normalized legacy `llmtypes.Usage` stream cannot express
presence/reasoning; metrics therefore use the explicit row rather than inferring
those properties from its zero-valued fields. No accounting metadata is hidden
in stream text. Missing reports produce unknown components, never measured zero.

`costcalc.PriceRow` prices that call's own immutable `PriceSnapshot`. A nil price
is unpriced; a known zero tariff is free; an unknown rate stays unpriced/partial.
The cost is a fixture estimate with unspecified cost kind, never a provider bill.
Catalog fetching, current prices, repricing old records and provider-reported
billing integration are outside this slice.

The scenario digest hashes the accepted JSON scenario, not original YAML bytes.
The rubric outcome, trajectory, usage, costs and virtual timing
are deterministic for identical inputs. Default virtual time uses the synthetic
host's fixed clock; `Options.Start` changes its starting instant. No sleeps or
real latency benchmarks occur. Event/HITL record IDs retain the existing
producers' opaque random IDs, so complete trace JSON is not promised byte-identical
between independent runs. JSON round-trip and evidence replay preserve a given
record exactly; callers own storing the artifact and its integrity.

## Unknown outcomes and replacement

`testdata/uncertain.yaml` simulates a tool which was admitted and then returns an
uncertain outcome. It records one fake invocation, an unknown execution receipt
and terminal failure without asking the model to retry. There is no live effect.
Trace replay returns only detached evidence values, with no callbacks or effect
ports. Replacement tests retain the SDK run reservation across host/attempt
replacement, refuse the old logical run ID before a new model call, and preserve
the old binding/unknown outcome during replay. Replay never reconciles or
retargets an effect.

This is the minimal deterministic SDK-3 foundation. Durable dataset/run stores,
baseline comparison, inspector/UI, LLM judges, Recall@k, rich graders, matrix
experiments, general transcript ingestion, production telemetry and application
integration remain open work.
