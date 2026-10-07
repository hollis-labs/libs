package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Frozen per-registry-version fixtures are historical wire inputs shared by Go and TS.
// Delete this bridge when either language is generated from one schema.
func classify(err error) string {
	for _, pair := range []struct {
		err  error
		name string
	}{{ErrRegistryVersion, "ErrRegistryVersion"}, {ErrInvalidContribution, "ErrInvalidContribution"}, {ErrUnknownPlugin, "ErrUnknownPlugin"}, {ErrIntegrity, "ErrIntegrity"}, {ErrRuntime, "ErrRuntime"}, {ErrCollision, "ErrCollision"}} {
		if errors.Is(err, pair.err) {
			return pair.name
		}
	}
	if err != nil {
		return "ErrInvalidContribution"
	}
	return "ok"
}
func TestContractFixtures(t *testing.T) {
	dir := filepath.Join("testdata", "contract", fmt.Sprintf("registry-v%d", RegistryVersion))
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no frozen contract fixtures")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fx struct {
				Response    json.RawMessage `json:"response"`
				ResponseRaw *string         `json:"response_raw"`
				Projection  *struct {
					Kind, Key, Status string
					StatusReason      string `json:"status_reason"`
					Diagnostics       []string
					Accepted          int
				} `json:"projection"`
				Go struct {
					Validate string `json:"validate"`
				} `json:"go"`
			}
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			var response Response
			payload := fx.Response
			if fx.ResponseRaw != nil {
				payload = []byte(*fx.ResponseRaw)
			}
			err = json.Unmarshal(payload, &response)
			if err == nil {
				err = response.Validate()
			}
			if got := classify(err); got != fx.Go.Validate {
				t.Fatalf("got %s want %s", got, fx.Go.Validate)
			}
			if err == nil && fx.Projection != nil {
				expectation := fx.Projection
				plan, planErr := response.Plan(policyFor(response))
				if planErr != nil {
					t.Fatal(planErr)
				}
				if len(plan.Listed) != 1 || len(plan.Accepted) != expectation.Accepted {
					t.Fatal("unexpected projection admission")
				}
				listed := plan.Listed[0]
				if listed.Status != ContributionStatus(expectation.Status) || listed.StatusReason != expectation.StatusReason {
					t.Fatal("status projection mismatch")
				}
				reasons := []string{}
				for _, diagnostic := range plan.StatusDiagnostics {
					reasons = append(reasons, diagnostic.Reason)
				}
				if !reflect.DeepEqual(reasons, expectation.Diagnostics) {
					t.Fatal("status diagnostics mismatch")
				}
				catalog := NewCatalog(response.HostInstance)
				activated, activationErr := catalog.Activate(response, policyFor(response))
				if activationErr != nil {
					t.Fatal(activationErr)
				}
				snapshot := catalog.Snapshot()
				entry := snapshot.Contributions[expectation.Kind][expectation.Key]
				if entry.Status != ContributionStatus(expectation.Status) || entry.StatusReason != expectation.StatusReason {
					t.Fatal("snapshot status mismatch")
				}
				published, _ := json.Marshal(struct {
					Plan      Plan
					Activated Plan
					Snapshot  Response
				}{plan, activated, snapshot})
				original := response.Contributions[expectation.Kind][expectation.Key].Status
				if bytes.Contains(published, []byte(original)) {
					t.Fatal("raw unknown status echoed into output")
				}
			}
			if err == nil {
				wire, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				var again Response
				if err = json.Unmarshal(wire, &again); err != nil {
					t.Fatal(err)
				}
				if err = again.Validate(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
