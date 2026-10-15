package render

import (
	"encoding/json"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"reflect"
	"testing"
)

func TestInstalledOwnedLeafContract(t *testing.T) {
	// Cairn install/merge.go and install/toml.go recursively replace declared
	// scalar/array leaves and preserve undeclared operator leaves. These fixtures
	// match the settings and server declarations in the archived installed seeds.
	for _, p := range []runtimes.ID{runtimes.Claude, runtimes.Codex} {
		req := request(p)
		req.Layer = layout.Installed
		req.Mode = layout.InstallMode
		fields := []layout.Field{layout.Settings}
		var want [][]string
		reserved := []string{}
		if p == runtimes.Claude {
			req.Native.Claude.Slots = []contract.Slot{{Key: "permissions", Value: map[string]string{"defaultMode": "acceptEdits", "operator_rule": "deny"}}, {Key: "fixture_operator", Value: true}}
			req.Native.OperatorKeyPaths = [][]string{{"fixture_operator"}, {"permissions", "operator_rule"}}
			want = [][]string{{"permissions", "defaultMode"}}
		} else {
			fields = append(fields, layout.MCP)
			req.Native.Codex.Slots = []contract.Slot{{Key: "approval_policy", Value: "on-request"}, {Key: "fixture_operator", Value: true}}
			req.Native.Servers = []contract.Server{{Name: "fixture", Command: "fixture-server", Args: []string{"one", "two"}}}
			req.Native.OperatorKeyPaths = [][]string{{"fixture_operator"}}
			want = [][]string{{"approval_policy"}, {"mcp_servers", "fixture", "args"}, {"mcp_servers", "fixture", "command"}}
			reserved = []string{"mcp_servers"}
		}
		for _, field := range fields {
			r, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: p, Layer: req.Layer, Mode: req.Mode, Field: field}, Requirement: layout.Required})
			if err != nil {
				t.Fatal(err)
			}
			req.Inputs = append(req.Inputs, Input{Resolved: r})
		}
		out, err := Render(req)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range out.Tree.Entries {
			if e.Kind != "file" {
				continue
			}
			var got DocumentOwnership
			if err = json.Unmarshal([]byte(e.Provenance.Note), &got); err != nil {
				t.Fatal(err)
			}
			if got.Schema != "native-key-ownership.v1" || !reflect.DeepEqual(got.OwnedKeyPaths, want) || !reflect.DeepEqual(got.ReservedSlots, reserved) {
				t.Fatalf("%s: %#v", p, got)
			}
		}
	}
}
