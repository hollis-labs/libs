package gateconnector_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/hollis-labs/libs/agent/sdk/gateconnector"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/hitl"
)

// This snapshot is a read-only contract observation, not a live service test.
// If Gatecrasher adds routes, update the pinned source and implement an actual
// authenticated client before advertising any GatePort capability.
func TestPinnedServiceHTTPContractRefusesMissingPortOperations(t *testing.T) {
	raw, err := os.ReadFile("testdata/gatecrasher-http-ac24a5c.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		SourceCommit string                                   `json:"source_commit"`
		Routes       []struct{ Method, Path, Purpose string } `json:"routes"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SourceCommit != "ac24a5c855146625eff79064d7cbf287bcecaf1b" {
		t.Fatal("unpinned service contract")
	}
	got := map[string]string{}
	for _, route := range snapshot.Routes {
		got[route.Method+" "+route.Path] = route.Purpose
	}
	want := map[string]string{
		"POST /api/gate/v1/authorize-response":  "pre-CAS response assessment",
		"POST /api/gate/v1/recipes/fingerprint": "bounded SDK recipe",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("service contract changed: %+v", got)
	}
	port := gateconnector.ServiceHTTPPort{}
	ctx := context.Background()
	if _, err := port.Capabilities(ctx, gate.AdapterScope{}); !errors.Is(err, gate.ErrUnsupported) {
		t.Fatalf("capabilities: %v", err)
	}
	if _, err := port.Request(ctx, gate.Request{}); !errors.Is(err, gate.ErrUnsupported) {
		t.Fatalf("request: %v", err)
	}
	if _, err := port.Await(ctx, gate.Ticket{}, gate.WaitOptions{}); !errors.Is(err, gate.ErrUnsupported) {
		t.Fatalf("await: %v", err)
	}
	if _, err := port.Cancel(ctx, gate.Ticket{}, hitl.CauseCallerWithdrawn); !errors.Is(err, gate.ErrUnsupported) {
		t.Fatalf("cancel: %v", err)
	}
}
