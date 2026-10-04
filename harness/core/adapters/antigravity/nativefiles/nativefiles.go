// Package nativefiles encodes Antigravity workspace documents from resolved inputs.
// It performs no I/O and never renders credentials or global configuration.
package nativefiles

import (
	"bytes"
	"encoding/json"

	"github.com/hollis-labs/substrate/harness/adapters/internal/owner"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
)

var serializerOwner = owner.New("antigravity")

// Owner returns the immutable serializer handle; it cannot mint another owner.
func Owner() contract.Owner { return serializerOwner }

// Instructions copies the already composed instruction body without adding bindings.
func Instructions(body []byte) []byte { return bytes.Clone(body) }

// Plugin preserves the stable plugin identity used for tool-schema caching.
func Plugin() []byte { return []byte("{\"name\":\"tether\"}\n") }

// MCPInput carries host bindings and their resolved transport mode.
type MCPInput struct {
	Mode    string
	Servers []contract.Server
}

// MCP encodes the plugin HTTP and stdio MCP transports.
func MCP(in MCPInput) ([]byte, error) {
	servers := in.Servers
	ctx := contract.Context{Provider: "antigravity", Mode: in.Mode, Concern: "mcp"}
	if err := contract.ValidateServers(ctx, servers); err != nil {
		return nil, err
	}
	entries := map[string]any{}
	for _, s := range servers {
		if s.HTTPURL != "" {
			entries[s.Name] = map[string]any{"serverUrl": s.HTTPURL}
			continue
		}
		args := s.Args
		if args == nil {
			args = []string{}
		}
		entry := map[string]any{"command": s.Command, "args": args}
		if len(s.Env) > 0 {
			env := map[string]string{}
			for _, v := range s.Env {
				env[v.Name] = v.Value
			}
			entry["env"] = env
		}
		entries[s.Name] = entry
	}
	out, err := json.MarshalIndent(map[string]any{"mcpServers": entries}, "", "  ")
	if err != nil {
		return nil, ctx.Refuse(contract.InvalidInput, "MCP cannot be encoded")
	}
	return append(out, '\n'), nil
}
