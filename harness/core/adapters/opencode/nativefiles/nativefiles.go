// Package nativefiles encodes OpenCode native documents from resolved inputs.
// It performs no I/O and never renders credential material. Modes come from
// the plan table: native config and MCP documents require 0600.
package nativefiles

import (
	"bytes"
	"encoding/json"

	"github.com/hollis-labs/substrate/harness/adapters/internal/owner"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
)

var serializerOwner = owner.New("opencode")

// Owner returns the immutable serializer handle; it cannot mint another owner.
func Owner() contract.Owner { return serializerOwner }

// AgentInput contains resolved identity and instruction content.
type AgentInput struct {
	Mode              string
	Name, Description string
	Body              []byte
}

// Agent encodes a primary agent document with safe front matter.
func Agent(in AgentInput) ([]byte, error) {
	ctx := contract.Context{Provider: "opencode", Mode: in.Mode, Concern: "instructions"}
	if err := contract.ValidateValue(ctx, []string{in.Name, in.Description}); err != nil {
		return nil, err
	}
	if in.Name == "" {
		return nil, ctx.Refuse(contract.InvalidInput, "agent name is empty")
	}
	for _, c := range in.Name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return nil, ctx.Refuse(contract.InvalidInput, "agent name must be a safe component")
		}
	}

	desc := in.Description
	if desc == "" {
		desc = "Launch agent " + in.Name
	}
	// JSON strings are YAML-compatible scalars and cannot inject front matter.
	scalar, _ := json.Marshal(desc)
	if in.Description == "" {
		scalar = []byte(desc)
	}
	var out bytes.Buffer
	out.WriteString("---\ndescription: ")
	out.Write(scalar)
	out.WriteString("\nmode: primary\n---\n\n")
	out.Write(in.Body)
	if len(in.Body) > 0 && !bytes.HasSuffix(in.Body, []byte("\n")) {
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

// ConfigInput composes native app slots with typed MCP bindings.
type ConfigInput struct {
	Mode    string
	Servers []contract.Server
	Slots   []contract.Slot
}

// Config encodes one native configuration document.
func Config(in ConfigInput) ([]byte, error) {
	ctx := contract.Context{Provider: "opencode", Mode: in.Mode, Concern: "settings"}
	if err := contract.ValidateServers(ctx, in.Servers); err != nil {
		return nil, err
	}
	for _, slot := range in.Slots {
		if slot.Key == "mcp" {
			return nil, ctx.Refuse(contract.DuplicateKey, "MCP native slot belongs to typed server bindings")
		}
	}
	generated := []contract.Slot{}
	if len(in.Servers) > 0 {
		entries := map[string]any{}
		for _, s := range in.Servers {
			if s.HTTPURL != "" {
				entries[s.Name] = map[string]any{"type": "remote", "url": s.HTTPURL, "enabled": true}
				continue
			}
			entry := map[string]any{"type": "local", "command": append([]string{s.Command}, s.Args...), "enabled": true}
			if len(s.Env) > 0 {
				env := map[string]string{}
				for _, v := range s.Env {
					env[v.Name] = v.Value
				}
				entry["environment"] = env
			}
			entries[s.Name] = entry
		}
		generated = append(generated, contract.Slot{Key: "mcp", Value: entries})
	}
	doc, err := contract.Object(ctx, in.Slots, generated)
	if err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, ctx.Refuse(contract.InvalidInput, "config cannot be encoded")
	}
	return append(out, '\n'), nil
}
