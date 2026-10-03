package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Frozen per-protocol fixtures are historical wire inputs shared by Go and TS.
// Delete this bridge when either language is generated from one schema.
func classify(err error) string {
	for _, pair := range []struct {
		err  error
		name string
	}{{ErrProtocol, "ErrProtocol"}, {ErrInvalidContribution, "ErrInvalidContribution"}, {ErrUnknownPlugin, "ErrUnknownPlugin"}, {ErrIntegrity, "ErrIntegrity"}, {ErrRuntime, "ErrRuntime"}, {ErrCollision, "ErrCollision"}} {
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
	dir := filepath.Join("testdata", "contract", fmt.Sprintf("protocol-%d", Protocol))
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
				Response json.RawMessage `json:"response"`
				Go       struct {
					Validate string `json:"validate"`
				} `json:"go"`
			}
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			var response Response
			err = json.Unmarshal(fx.Response, &response)
			if err == nil {
				err = response.Validate()
			}
			if got := classify(err); got != fx.Go.Validate {
				t.Fatalf("got %s want %s", got, fx.Go.Validate)
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
