// Package consumer_test is a clean downstream module fixture. It imports only
// public go-workflow packages and the Go standard library.
//
// WARNING: runtime/inmemory is process-lifetime storage. This fixture proves
// public API integration only; it does not prove durable restart or production
// recovery behavior. Production hosts must provide durable runtime stores.
package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hollis-labs/go-workflow/compile"
	"github.com/hollis-labs/go-workflow/diagnostic"
	"github.com/hollis-labs/go-workflow/graph"
	"github.com/hollis-labs/go-workflow/offline"
	workflowruntime "github.com/hollis-labs/go-workflow/runtime"
	"github.com/hollis-labs/go-workflow/runtime/inmemory"
	"github.com/hollis-labs/go-workflow/stepkind"
	"github.com/hollis-labs/go-workflow/stepkind/stepkindtest"
	"github.com/hollis-labs/go-workflow/values"
)

const workflowSource = `workflow:
  name: External Consumer
  version: 1.0.0
inputs:
  - name: value
    type: integer
    required: true
steps:
  - id: copy
    kind: external_copy
    kind_version: v1
    with:
      value:
        expression: inputs.value
    outputs:
      value:
        type: integer
outputs:
  result:
    type: integer
    value:
      expression: steps.copy.outputs.value
`

func TestCompileAndExecuteWithExplicitlyNonDurableStore(t *testing.T) {
	t.Log("WARNING: runtime/inmemory is non-durable and must not be used to claim production recovery")

	loaded := compile.LoadBytes("external-consumer.workflow.yaml", []byte(workflowSource))
	if loaded.Source == nil || hasErrors(loaded.Diagnostics) {
		t.Fatalf("load source: %#v", loaded.Diagnostics)
	}

	compiled := compile.Compile(loaded.Source)
	if compiled.Plan == nil || hasErrors(compiled.Diagnostics) {
		t.Fatalf("compile source: %#v", compiled.Diagnostics)
	}

	inferred := compile.InferValueDependencies(compiled.Plan, compile.DependencyOptions{})
	if inferred.Plan == nil || hasErrors(inferred.Diagnostics) {
		t.Fatalf("infer dependencies: %#v", inferred.Diagnostics)
	}

	registry := stepkind.NewRegistry()
	copyKind := stepkindtest.NewNoopKind("external_copy", "v1")
	copyKind.SpecValue.InputSchema = graph.Schema{"type": "object"}
	copyKind.SpecValue.OutputSchema = graph.Schema{"type": "object"}
	copyKind.ExecuteFunc = func(_ context.Context, invocation stepkind.PreparedInvocation) (stepkind.StepResult, error) {
		input, ok := invocation.Invocation.Inputs["value"]
		if !ok {
			return stepkind.StepResult{}, errors.New("value input is missing")
		}
		output, err := values.NewInline(input.Inline, values.Metadata{
			Producer: values.Producer{
				Kind:      "external-consumer",
				Reference: invocation.Invocation.Identity.NodeID,
				Output:    "value",
			},
			MediaType: "application/json",
			Redaction: values.RedactionPublic,
			Retention: values.RetentionRun,
		})
		if err != nil {
			return stepkind.StepResult{}, err
		}
		return stepkind.StepResult{
			Outcome: stepkind.StepCompleted,
			Outputs: values.ValueSet{"value": output},
		}, nil
	}
	if err := registry.Register(copyKind); err != nil {
		t.Fatal(err)
	}

	if findings := compile.ValidatePlan(t.Context(), inferred.Plan, compile.ValidationOptions{StepKinds: registry}); hasErrors(findings) {
		t.Fatalf("validate plan: %#v", findings)
	}

	built, err := offline.Build(t.Context(), inferred.Plan, offline.BuildOptions{
		Registry: registry,
		Mode:     offline.ModeCLI,
	})
	if err != nil || built.Manifest == nil || hasErrors(built.Diagnostics) {
		t.Fatalf("build manifest: diagnostics=%#v err=%v", built.Diagnostics, err)
	}

	store := inmemory.NewStore()
	executed, err := offline.ExecuteWithStore(t.Context(), *built.Manifest, offline.ExecuteOptions{
		Registry:       registry,
		Inputs:         map[string]any{"value": 7},
		RunID:          "external-consumer-run",
		IdempotencyKey: "external-consumer-start-v1",
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	if executed.Run.Status != workflowruntime.RunSucceeded {
		t.Fatalf("run status = %q, want %q", executed.Run.Status, workflowruntime.RunSucceeded)
	}
	if got := executed.Outputs["result"].Inline; got != json.Number("7") {
		t.Fatalf("result = %#v, want json.Number(7)", got)
	}
}

func hasErrors(findings []diagnostic.Diagnostic) bool {
	for _, finding := range findings {
		if finding.Severity == diagnostic.SeverityError {
			return true
		}
	}
	return false
}
