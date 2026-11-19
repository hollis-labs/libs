package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/mcptest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExampleWire(t *testing.T) {
	b, err := os.ReadFile("card.json")
	if err != nil {
		t.Fatal(err)
	}
	var card map[string]any
	if err = json.Unmarshal(b, &card); err != nil {
		t.Fatal(err)
	}
	s, err := newServer(card, "https://gate.example", "https://intake.example")
	if err != nil {
		t.Fatal(err)
	}
	cs, cleanup := mcptest.Connect(t, s)
	defer cleanup()
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	uri := tools.Tools[0].Meta["ui"].(map[string]any)["resourceUri"].(string)
	r, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Contents[0].Text, "ui/initialize") || strings.Contains(r.Contents[0].Text, "/*SCRIPT*/") {
		t.Fatal("app not bundled")
	}
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "show_approval_card", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("call: %v %v", result, err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), "gatecrasher.card/v1") {
		t.Fatal("missing card")
	}
}

func TestClientSecretBoundary(t *testing.T) {
	// Run the actual bundled browser code, with a small DOM and real HTTP APIs.
	// Node is required, rather than skipping the task's secret-boundary gate.
	cmd := exec.Command("node", "--test", "app.test.cjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("client conformance failed: %s", output)
	}
	t.Logf("client conformance: %s", output)
}
