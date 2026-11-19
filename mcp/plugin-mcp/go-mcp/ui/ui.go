// Package ui supplies the server-side transport metadata for the optional
// MCP Apps extension. Rendering and client credentials belong to the host.
package ui

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	Extension = "io.modelcontextprotocol/ui"
	MIMEType  = "text/html;profile=mcp-app"
)

// Capabilities returns fresh capabilities advertising MCP Apps. Merge the
// Extensions entry into existing capabilities when a server uses other extensions.
func Capabilities() *mcp.ServerCapabilities {
	return &mcp.ServerCapabilities{Extensions: map[string]any{Extension: map[string]any{"mimeTypes": []string{MIMEType}}}}
}

// Supports reports whether the client explicitly supports HTML MCP Apps.
func Supports(caps *mcp.ClientCapabilities) bool {
	if caps == nil {
		return false
	}
	b, ok := caps.Extensions[Extension].(map[string]any)
	if !ok {
		return false
	}
	switch types := b["mimeTypes"].(type) {
	case []string:
		for _, t := range types {
			if t == MIMEType {
				return true
			}
		}
	case []any:
		for _, t := range types {
			if t == MIMEType {
				return true
			}
		}
	}
	return false
}

func validateURI(uri string) error {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "ui" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("ui: expected an absolute ui:// resource URI")
	}
	return nil
}

// Attach returns a tool with _meta.ui.resourceUri, preserving other metadata.
// Nested metadata values must remain immutable after registration.
func Attach(tool server.Tool, uri string) (server.Tool, error) {
	if err := validateURI(uri); err != nil {
		return tool, err
	}
	meta := make(map[string]any, len(tool.Meta)+1)
	for k, v := range tool.Meta {
		meta[k] = v
	}
	settings := map[string]any{}
	if old, ok := meta["ui"].(map[string]any); ok {
		for k, v := range old {
			settings[k] = v
		}
	}
	settings["resourceUri"] = uri
	meta["ui"] = settings
	tool.Meta = meta
	return tool, nil
}

// Resource is a bundled HTML app. ConnectDomains is the explicit browser
// network allowlist (_meta.ui.csp.connectDomains); no origins are added implicitly.
// Hosts enforce CSP and may further restrict it. The app still needs CORS at APIs.
type Resource struct {
	URI            string
	Name           string
	HTML           string
	ConnectDomains []string
}

// Register publishes a resource through the underlying SDK. It does not install
// tools, mutate capabilities, or implement the host's sandbox or AppBridge.
func Register(s *server.Server, resource Resource) error {
	if err := validateURI(resource.URI); err != nil {
		return err
	}
	domains := append([]string{}, resource.ConnectDomains...)
	for _, origin := range domains {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("ui: connect domain must be an HTTP(S) origin")
		}
	}
	meta := mcp.Meta{"ui": map[string]any{"csp": map[string]any{"connectDomains": domains}}}
	s.SDKServer().AddResource(&mcp.Resource{URI: resource.URI, Name: resource.Name, MIMEType: MIMEType}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: resource.URI, MIMEType: MIMEType, Text: resource.HTML, Meta: meta}}}, nil
	})
	return nil
}
