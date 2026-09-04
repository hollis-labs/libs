package compatguard

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	workflowcompile "github.com/hollis-labs/go-workflow/compile"
	graphschema "github.com/hollis-labs/go-workflow/graph/schema"
)

const (
	graphSchemaSHA256 = "4f2ba8ee73fbcb63e4628586342cb3a2d0329b58daa420a5a0c9610d1e5f12dc"
	planSchemaSHA256  = "639e768982a2710f96ddfd16d090557a0c5ea1db65fc894a09da2ddf96353a65"

	representativePlanDigest  = "sha256:783142979a13c77f4e6d19d4ad2a94af1ac220d96fbbb092e0412e04af6e7e65"
	representativeGraphDigest = "sha256:e611774cde5720cfa3d63f9fa3e6df5f23cef608ce46faf2f96f5939c15b4b67"
)

func TestV01SchemaAndDigestGoldens(t *testing.T) {
	root := moduleRoot(t)
	assertFileDigest(t, filepath.Join(root, "graph/schema/workflow.schema.json"), graphSchemaSHA256)
	assertFileDigest(t, filepath.Join(root, "compile/schema/execution-plan.schema.json"), planSchemaSHA256)

	if graphschema.ID != "https://schemas.hollis-labs.dev/workflow/graph/v1/workflow.schema.json" {
		t.Fatalf("graph schema identity changed: %q", graphschema.ID)
	}
	if workflowcompile.ExecutionPlanSchemaVersion != "1" {
		t.Fatalf("execution plan schema version changed: %q", workflowcompile.ExecutionPlanSchemaVersion)
	}

	data, err := os.ReadFile(filepath.Join(root, "compile/testdata/snapshots/representative.plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		SchemaVersion string `json:"schema_version"`
		Digest        string `json:"digest"`
		Graph         struct {
			Digest string `json:"digest"`
		} `json:"graph"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != workflowcompile.ExecutionPlanSchemaVersion ||
		snapshot.Digest != representativePlanDigest || snapshot.Graph.Digest != representativeGraphDigest {
		t.Fatalf("representative digest golden changed: schema=%q plan=%q graph=%q", snapshot.SchemaVersion, snapshot.Digest, snapshot.Graph.Digest)
	}
}

func assertFileDigest(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
		t.Fatalf("%s digest = %s, want %s; review STABILITY.md before updating the golden", path, got, want)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate compatibility guard")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}
