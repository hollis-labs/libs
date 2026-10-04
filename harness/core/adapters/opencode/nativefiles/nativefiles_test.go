package nativefiles

import (
	"bytes"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"testing"
)

func TestDocuments(t *testing.T) {
	out, err := Agent(AgentInput{Name: "fixture", Body: []byte("Resolved instructions.\n")})
	if err != nil || !bytes.Contains(out, []byte("mode: primary\n")) || !bytes.Contains(out, []byte("Resolved instructions.")) {
		t.Fatalf("%s %v", out, err)
	}
	if _, err = Agent(AgentInput{Name: "x\nmode: hidden"}); err == nil {
		t.Fatal("frontmatter injection")
	}
	out, err = Config(ConfigInput{})
	if err != nil || string(out) != "{}\n" {
		t.Fatalf("%s %v", out, err)
	}
	out, err = Config(ConfigInput{Servers: []contract.Server{{Name: "http", HTTPURL: "http://example.invalid"}, {Name: "stdio", Command: "fixture", Args: []string{"one"}}}})
	if err != nil || !bytes.Contains(out, []byte(`"type": "remote"`)) || !bytes.Contains(out, []byte(`"type": "local"`)) {
		t.Fatalf("%s %v", out, err)
	}
	if _, err = Config(ConfigInput{Servers: []contract.Server{{Name: "x", Command: "fixture"}}, Slots: []contract.Slot{{Key: "mcp", Value: map[string]any{"x": map[string]any{"type": "remote"}}}}}); err == nil {
		t.Fatal("duplicate MCP owner")
	}
}
