package ui_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/mcptest"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/ui"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWireContract(t *testing.T) {
	for _, pagination := range []bool{false, true} {
		t.Run(map[bool]string{false: "SDK", true: "catalog"}[pagination], func(t *testing.T) {
			opts := []server.Option{server.WithCapabilities(ui.Capabilities())}
			if pagination {
				opts = append(opts, server.WithToolsListPagination(1, nil))
			}
			s := server.NewServer("ui-test", "1", opts...)
			tool, err := ui.Attach(server.Tool{Name: "card", AlwaysLoad: true, Meta: map[string]any{"other": "kept", "ui": map[string]any{"visibility": []string{"model", "app"}}}, InputSchema: server.EmptyObjectSchema(), Handler: func(context.Context, map[string]any) (any, error) { return map[string]any{"status": "pending"}, nil }}, "ui://test/card")
			if err != nil {
				t.Fatal(err)
			}
			s.RegisterTool(tool)
			if err = ui.Register(s, ui.Resource{URI: "ui://test/card", Name: "card", HTML: "<!doctype html><p>Card</p>", ConnectDomains: []string{"https://api.example"}}); err != nil {
				t.Fatal(err)
			}
			caps := &mcp.ClientCapabilities{Extensions: map[string]any{ui.Extension: map[string]any{"mimeTypes": []string{ui.MIMEType}}}}
			cs, cleanup := mcptest.Connect(t, s, mcptest.WithClientOptions(&mcp.ClientOptions{Capabilities: caps}))
			defer cleanup()
			if raw, _ := json.Marshal(cs.InitializeResult().Capabilities.Extensions[ui.Extension]); string(raw) != "{}" {
				t.Fatal("missing extension")
			}
			list, err := cs.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			meta := list.Tools[0].Meta
			u := meta["ui"].(map[string]any)
			if u["resourceUri"] != "ui://test/card" || meta["other"] != "kept" || meta[server.AlwaysLoadMetaKey] != true {
				t.Fatalf("metadata: %v", meta)
			}
			if s.ToolDefinitions()[0].Meta["ui"] == nil {
				t.Fatal("definition dropped metadata")
			}
			resources, err := cs.ListResources(context.Background(), nil)
			if err != nil || len(resources.Resources) != 1 {
				t.Fatalf("resources: %v %v", resources, err)
			}
			r, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: u["resourceUri"].(string)})
			if err != nil {
				t.Fatal(err)
			}
			c := r.Contents[0]
			if c.URI != u["resourceUri"] || c.MIMEType != ui.MIMEType || c.Text != "<!doctype html><p>Card</p>" {
				t.Fatalf("resource: %+v", c)
			}
			data, _ := json.Marshal(c.Meta)
			if string(data) != `{"ui":{"csp":{"connectDomains":["https://api.example"]}}}` {
				t.Fatalf("CSP: %s", data)
			}
		})
	}
}
func TestValidationAndSupport(t *testing.T) {
	for _, uri := range []string{"https://test/card", "ui:card", "ui:///card", "ui://user@test/card"} {
		if _, err := ui.Attach(server.Tool{}, uri); err == nil {
			t.Errorf("accepted %q", uri)
		}
	}
	for _, domains := range [][]string{{"*"}, {"https://api.example/path"}, {"https://api.example?token=x"}} {
		if err := ui.Register(server.NewServer("x", "1"), ui.Resource{URI: "ui://test/card", ConnectDomains: domains}); err == nil {
			t.Errorf("accepted %v", domains)
		}
	}
	if ui.Supports(nil) || ui.Supports(&mcp.ClientCapabilities{}) {
		t.Fatal("implicit support")
	}
	for _, types := range []any{[]string{ui.MIMEType}, []any{ui.MIMEType}} {
		if !ui.Supports(&mcp.ClientCapabilities{Extensions: map[string]any{ui.Extension: map[string]any{"mimeTypes": types}}}) {
			t.Fatal("explicit support not recognized")
		}
	}
}

func TestOptInAndPerRequestClientCapability(t *testing.T) {
	plain := server.NewServer("plain", "1")
	cs, cleanup := mcptest.Connect(t, plain)
	if cs.InitializeResult().Capabilities.Extensions[ui.Extension] != nil {
		t.Fatal("Apps enabled implicitly")
	}
	cleanup()
	for _, enabled := range []bool{false, true} {
		s := server.NewServer("cap-test", "1", server.WithCapabilities(ui.Capabilities()))
		s.RegisterTool(server.Tool{Name: "capability", InputSchema: server.EmptyObjectSchema(), Handler: func(ctx context.Context, _ map[string]any) (any, error) {
			return map[string]any{"supported": ui.Supports(server.ClientCapabilities(ctx))}, nil
		}})
		caps := &mcp.ClientCapabilities{}
		if enabled {
			caps.Extensions = map[string]any{ui.Extension: map[string]any{"mimeTypes": []string{ui.MIMEType}}}
		}
		client, done := mcptest.Connect(t, s, mcptest.WithClientOptions(&mcp.ClientOptions{Capabilities: caps}))
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "capability", Arguments: map[string]any{}})
		done()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(result.StructuredContent)
		want := `{"supported":false}`
		if enabled {
			want = `{"supported":true}`
		}
		if string(data) != want {
			t.Fatalf("per-request capability: %s, want %s", data, want)
		}
	}
}
