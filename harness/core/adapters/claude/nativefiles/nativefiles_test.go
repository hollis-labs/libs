package nativefiles

import (
	"bytes"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"testing"
)

func TestDocuments(t *testing.T) {
	input := []byte("Resolved instructions.\n")
	got := Instructions(input)
	got[0] = 'X'
	if input[0] == 'X' {
		t.Fatal("aliased instructions")
	}
	if string(Pointer()) != "@AGENTS.md\n" {
		t.Fatal("pointer")
	}
	out, err := Settings(SettingsInput{Permission: &Permission{DefaultMode: "plan", AdditionalDirectories: []string{"/fixture/project"}}, Slots: []contract.Slot{{Key: "model", Value: "fixture"}}})
	if err != nil || !bytes.Contains(out, []byte(`"defaultMode": "plan"`)) {
		t.Fatalf("%s %v", out, err)
	}
	if _, err = Settings(SettingsInput{Permission: &Permission{DefaultMode: "invented"}}); err == nil {
		t.Fatal("invalid mode")
	}
	if _, err = Settings(SettingsInput{Permission: &Permission{DefaultMode: "plan"}, Slots: []contract.Slot{{Key: "permissions", Value: map[string]any{"defaultMode": "default"}}}}); err == nil {
		t.Fatal("duplicate permission")
	}
	out, err = MCP(MCPInput{})
	if err != nil || string(out) != "{\"mcpServers\":{}}\n" {
		t.Fatalf("%s %v", out, err)
	}
	out, err = MCP(MCPInput{Servers: []contract.Server{{Name: "http", HTTPURL: "http://example.invalid"}, {Name: "stdio", Command: "fixture"}}})
	if err != nil || !bytes.Contains(out, []byte(`"type": "http"`)) || !bytes.Contains(out, []byte(`"args": []`)) {
		t.Fatalf("%s %v", out, err)
	}
}
