// Package nativefiles encodes Claude native documents from resolved inputs.
// It performs no I/O and never renders credential material.
package nativefiles

import (
	"bytes"
	"encoding/json"

	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
)

// Instructions copies the already composed instruction body without adding bindings.
func Instructions(body []byte) []byte { return bytes.Clone(body) }

// Pointer encodes the explicit neutral-instructions discovery option.
func Pointer() []byte { return []byte("@AGENTS.md\n") }

// Permission holds native permission settings, supplied after posture binding.
type Permission struct {
	DefaultMode           string
	AdditionalDirectories []string
}

// SettingsInput composes app slots with explicit generated native settings.
type SettingsInput struct {
	Mode         string
	APIKeyHelper string
	Permission   *Permission
	Slots        []contract.Slot
}

// Settings encodes one settings document, refusing colliding native leaves.
func Settings(in SettingsInput) ([]byte, error) {
	ctx := contract.Context{Provider: "claude", Mode: in.Mode, Concern: "settings"}
	generated := []contract.Slot{}
	if in.APIKeyHelper != "" {
		generated = append(generated, contract.Slot{Key: "apiKeyHelper", Value: in.APIKeyHelper})
	}
	if p := in.Permission; p != nil {
		doc := map[string]any{}
		switch p.DefaultMode {
		case "":
		case "default", "acceptEdits", "plan", "bypassPermissions":
			doc["defaultMode"] = p.DefaultMode
		default:
			return nil, ctx.Refuse(contract.InvalidInput, "unknown native permission mode")
		}
		if len(p.AdditionalDirectories) > 0 {
			doc["additionalDirectories"] = p.AdditionalDirectories
		}
		if len(doc) > 0 {
			generated = append(generated, contract.Slot{Key: "permissions", Value: doc})
		}
	}
	doc, err := contract.Object(ctx, in.Slots, generated)
	if err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, ctx.Refuse(contract.InvalidInput, "settings cannot be encoded")
	}
	return append(out, '\n'), nil
}

// MCPInput carries host bindings and their resolved transport mode.
type MCPInput struct {
	Mode    string
	Servers []contract.Server
}

// MCP encodes the Claude HTTP and stdio MCP transports.
func MCP(in MCPInput) ([]byte, error) {
	servers := in.Servers
	ctx := contract.Context{Provider: "claude", Mode: in.Mode, Concern: "mcp"}
	if err := contract.ValidateServers(ctx, servers); err != nil {
		return nil, err
	}
	entries := map[string]any{}
	for _, s := range servers {
		if s.HTTPURL != "" {
			entries[s.Name] = map[string]any{"type": "http", "url": s.HTTPURL}
			continue
		}
		args := s.Args
		if args == nil {
			args = []string{}
		}
		entry := map[string]any{"type": "stdio", "command": s.Command, "args": args}
		if len(s.Env) > 0 || s.Name == "mux" {
			env := map[string]string{}
			for _, v := range s.Env {
				env[v.Name] = v.Value
			}
			entry["env"] = env
		}
		entries[s.Name] = entry
	}
	if len(entries) == 0 {
		return []byte("{\"mcpServers\":{}}\n"), nil
	}
	out, err := json.MarshalIndent(map[string]any{"mcpServers": entries}, "", "  ")
	if err != nil {
		return nil, ctx.Refuse(contract.InvalidInput, "MCP cannot be encoded")
	}
	return append(out, '\n'), nil
}
