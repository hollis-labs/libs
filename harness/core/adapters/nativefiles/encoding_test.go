package nativefiles_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agy "github.com/hollis-labs/substrate/harness/adapters/antigravity/nativefiles"
	claude "github.com/hollis-labs/substrate/harness/adapters/claude/nativefiles"
	codex "github.com/hollis-labs/substrate/harness/adapters/codex/nativefiles"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	oc "github.com/hollis-labs/substrate/harness/adapters/opencode/nativefiles"
)

func TestTOMLStringBytes(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"<x>&>", `"<x>&>"`}, {"quote\"", `"quote\""`}, {"line\nnext", `"line\nnext"`}, {"a\\b", `"a\\b"`}, {"a\tb", `"a\tb"`}, {"café", `"café"`}, {"a\x7fb", `"a\u007fb"`},
	} {
		out, err := codex.Config(codex.ConfigInput{Slots: []contract.Slot{{Key: "value", Value: tc.input}}})
		if err != nil || string(out) != "value = "+tc.want+"\n" {
			t.Errorf("encoding got %q, want %q; %v", out, tc.want, err)
		}
	}
}
func TestInvalidUTF8AndOpaqueRefusals(t *testing.T) {
	bad := string([]byte{0xff})
	for _, tc := range []struct {
		provider, concern string
		render            func() ([]byte, error)
	}{
		{"codex", "settings", func() ([]byte, error) {
			return codex.Config(codex.ConfigInput{Mode: "subprocess-per-turn", Slots: []contract.Slot{{Key: "value", Value: bad}}})
		}},
		{"claude", "settings", func() ([]byte, error) {
			return claude.Settings(claude.SettingsInput{Mode: "subprocess-per-turn", Slots: []contract.Slot{{Key: "value", Value: map[string]any{"nested": bad}}}})
		}},
		{"opencode", "settings", func() ([]byte, error) {
			return oc.Config(oc.ConfigInput{Mode: "subprocess-per-turn", Slots: []contract.Slot{{Key: "value", Value: []string{bad}}}})
		}},
		{"claude", "mcp", func() ([]byte, error) {
			return claude.MCP(claude.MCPInput{Mode: "subprocess-per-turn", Servers: []contract.Server{{Name: "x", Command: "fixture", Args: []string{bad}}}})
		}},
		{"antigravity", "mcp", func() ([]byte, error) {
			return agy.MCP(agy.MCPInput{Mode: "subprocess-per-turn", Servers: []contract.Server{{Name: "x", HTTPURL: bad}}})
		}},
		{"opencode", "instructions", func() ([]byte, error) {
			return oc.Agent(oc.AgentInput{Mode: "subprocess-per-turn", Name: "fixture", Description: bad})
		}},
	} {
		out, err := tc.render()
		var d *contract.Refusal
		if len(out) > 0 || !errors.As(err, &d) || d.Provider != tc.provider || d.Mode != "subprocess-per-turn" || d.Concern != tc.concern || d.Reason == "" {
			t.Errorf("%s/%s: out=%q err=%v", tc.provider, tc.concern, out, err)
		}
	}
	secret := "DUMMY-SENTINEL-PRIVATE-VALUE"
	for _, render := range []func() ([]byte, error){
		func() ([]byte, error) {
			return codex.Config(codex.ConfigInput{Mode: "subprocess-per-turn", Servers: []contract.Server{{Name: "x", Command: "fixture", Env: []contract.Variable{{Name: "A", Value: secret}, {Name: "A", Value: secret}}}}})
		},
		func() ([]byte, error) {
			return claude.MCP(claude.MCPInput{Servers: []contract.Server{{Name: "x", Command: secret, HTTPURL: secret}}})
		},
		func() ([]byte, error) {
			return oc.Config(oc.ConfigInput{Slots: []contract.Slot{{Key: "x", Value: secret}, {Key: "x", Value: secret}}})
		},
		func() ([]byte, error) {
			return agy.MCP(agy.MCPInput{Servers: []contract.Server{{Name: "x", Command: secret}, {Name: "x", Command: secret}}})
		},
	} {
		_, err := render()
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("opaque refusal failed: %v", err)
		}
	}
	_, err := codex.Config(codex.ConfigInput{Mode: "subprocess-per-turn", Servers: []contract.Server{{Name: "x", Command: "a"}, {Name: "x", Command: "b"}}})
	var d *contract.Refusal
	if !errors.As(err, &d) || d.Concern != "mcp" {
		t.Fatalf("MCP concern: %v", err)
	}
}
func TestJSONExactBytesAndDeterminism(t *testing.T) {
	servers := []contract.Server{{Name: "stdio", Command: "fixture"}}
	cases := []struct {
		render func() ([]byte, error)
		want   string
	}{
		{func() ([]byte, error) { return claude.MCP(claude.MCPInput{Servers: servers}) }, "{\n  \"mcpServers\": {\n    \"stdio\": {\n      \"args\": [],\n      \"command\": \"fixture\",\n      \"type\": \"stdio\"\n    }\n  }\n}\n"},
		{func() ([]byte, error) { return oc.Config(oc.ConfigInput{Servers: servers}) }, "{\n  \"mcp\": {\n    \"stdio\": {\n      \"command\": [\n        \"fixture\"\n      ],\n      \"enabled\": true,\n      \"type\": \"local\"\n    }\n  }\n}\n"},
		{func() ([]byte, error) { return agy.MCP(agy.MCPInput{Servers: servers}) }, "{\n  \"mcpServers\": {\n    \"stdio\": {\n      \"args\": [],\n      \"command\": \"fixture\"\n    }\n  }\n}\n"},
		{func() ([]byte, error) { return claude.Settings(claude.SettingsInput{}) }, "{}\n"},
	}
	for _, tc := range cases {
		for i := 0; i < 20; i++ {
			out, err := tc.render()
			if err != nil || string(out) != tc.want {
				t.Fatalf("byte mismatch: %s; %v", out, err)
			}
		}
	}
	a := []contract.Server{{Name: "b", Command: "b"}, {Name: "a", HTTPURL: "http://example.invalid"}}
	b := []contract.Server{a[1], a[0]}
	for _, render := range []func([]contract.Server) ([]byte, error){func(s []contract.Server) ([]byte, error) { return claude.MCP(claude.MCPInput{Servers: s}) }, func(s []contract.Server) ([]byte, error) { return oc.Config(oc.ConfigInput{Servers: s}) }, func(s []contract.Server) ([]byte, error) { return agy.MCP(agy.MCPInput{Servers: s}) }} {
		x, e := render(a)
		y, f := render(b)
		if e != nil || f != nil || !bytes.Equal(x, y) {
			t.Fatal("JSON order affected output")
		}
	}
	out, err := claude.MCP(claude.MCPInput{Servers: []contract.Server{{Name: "mux", Command: "fixture"}}})
	if err != nil || bytes.Contains(out, []byte(`"env"`)) {
		t.Fatalf("mux quirk: %s %v", out, err)
	}
}
func TestDirectoryGrantsRefuseUnsafePaths(t *testing.T) {
	for _, dir := range []string{"relative", "..", "/fixture/../escape"} {
		if _, err := claude.Settings(claude.SettingsInput{Permission: &claude.Permission{AdditionalDirectories: []string{dir}}}); err == nil {
			t.Errorf("Claude accepted %q", dir)
		}
		if _, err := codex.Config(codex.ConfigInput{WritableRoots: []string{dir}}); err == nil {
			t.Errorf("Codex accepted %q", dir)
		}
	}
	bad := json.RawMessage([]byte{'"', 0xff, '"'})
	if _, err := codex.Config(codex.ConfigInput{Slots: []contract.Slot{{Key: "value", Value: bad}}}); err == nil {
		t.Fatal("invalid raw JSON UTF-8")
	}
}

func TestNativeSlotDirectoryGrantsCannotBypassValidation(t *testing.T) {
	if _, err := claude.Settings(claude.SettingsInput{Slots: []contract.Slot{{Key: "permissions", Value: map[string]any{"additionalDirectories": []string{"relative"}}}}}); err == nil {
		t.Fatal("Claude raw slot bypass")
	}
	if _, err := codex.Config(codex.ConfigInput{Slots: []contract.Slot{{Key: "sandbox_workspace_write", Value: map[string]any{"writable_roots": []string{".."}}}}}); err == nil {
		t.Fatal("Codex raw slot bypass")
	}
}
func TestRealLeafHandlesCompose(t *testing.T) {
	ctx := contract.Context{Provider: "codex", Mode: "subprocess-per-turn", Concern: "composition"}
	handle := codex.Owner()
	if err := contract.ValidateComposition(ctx, []contract.Claim{contract.SerializerClaim("config.toml", handle, "native"), contract.SerializerClaim("./config.toml", handle, "native")}, []string{"config.toml"}); err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateComposition(ctx, []contract.Claim{contract.OverlayClaim(contract.Overlay{Path: "config.toml", Owner: handle})}, []string{"config.toml"}); err == nil {
		t.Fatal("overlay acquired serializer authority")
	}
}
