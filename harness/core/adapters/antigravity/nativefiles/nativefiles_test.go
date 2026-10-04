package nativefiles

import (
	"bytes"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"testing"
)

func TestDocuments(t *testing.T) {
	if string(Plugin()) != "{\"name\":\"tether\"}\n" {
		t.Fatal("plugin identity changed")
	}
	out, err := MCP(MCPInput{Servers: []contract.Server{{Name: "http", HTTPURL: "http://example.invalid"}, {Name: "stdio", Command: "fixture"}}})
	if err != nil || !bytes.Contains(out, []byte(`"serverUrl"`)) || !bytes.Contains(out, []byte(`"args": []`)) {
		t.Fatalf("%s %v", out, err)
	}
	if _, err = MCP(MCPInput{Servers: []contract.Server{{Name: "x", Command: "fixture"}, {Name: "x", Command: "other"}}}); err == nil {
		t.Fatal("duplicate MCP")
	}
	input := []byte("body")
	out = Instructions(input)
	out[0] = 'X'
	if input[0] == 'X' {
		t.Fatal("aliased content")
	}
}
