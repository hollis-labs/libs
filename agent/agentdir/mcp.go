package agentdir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// MCPServer describes one MCP transport.
type MCPServer struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// MCPConfig contains named MCP servers.
type MCPConfig struct{ Servers map[string]MCPServer }

// ParseMCP reads Claude-shaped MCP settings.
func ParseMCP(raw []byte) (MCPConfig, error) {
	var out MCPConfig
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return out, fmt.Errorf("agentdir: .mcp.json: %w", err)
	}
	if root == nil {
		return out, fmt.Errorf("agentdir: .mcp.json must be an object")
	}
	if root["mcpServers"] == nil {
		return out, nil
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(root["mcpServers"], &servers); err != nil {
		return out, fmt.Errorf("agentdir: mcpServers: %w", err)
	}
	if servers == nil {
		return out, fmt.Errorf("agentdir: mcpServers must be an object")
	}
	out.Servers = make(map[string]MCPServer, len(servers))
	for name, data := range servers {
		if strings.TrimSpace(name) == "" {
			return MCPConfig{}, fmt.Errorf("agentdir: empty MCP server name")
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		var server MCPServer
		if err := dec.Decode(&server); err != nil {
			return MCPConfig{}, fmt.Errorf("agentdir: MCP server %q: %w", name, err)
		}
		if server.Type == "" && server.Command != "" {
			server.Type = "stdio"
		}
		switch server.Type {
		case "stdio":
			if strings.TrimSpace(server.Command) == "" || server.URL != "" || len(server.Headers) != 0 {
				return MCPConfig{}, fmt.Errorf("agentdir: MCP server %q requires a stdio command", name)
			}
		case "http", "sse":
			parsed, err := url.Parse(server.URL)
			if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || server.Command != "" || len(server.Args) != 0 || len(server.Env) != 0 {
				return MCPConfig{}, fmt.Errorf("agentdir: MCP server %q requires an HTTP(S) URL", name)
			}
		default:
			return MCPConfig{}, fmt.Errorf("agentdir: unsupported transport for MCP server %q", name)
		}
		out.Servers[name] = server
	}
	return out, nil
}
