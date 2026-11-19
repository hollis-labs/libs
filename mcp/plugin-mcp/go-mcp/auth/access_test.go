package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/auth"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/budget"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
	httptransport "github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/transport/http"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

func TestAuthorizeToolsOverHTTP(t *testing.T) {
	calls := 0
	policyCalls := 0
	policy := func(ctx context.Context, info *sdkauth.TokenInfo, def server.ToolDefinition) bool {
		policyCalls++
		return info.UserID == "alice" && def.Name == "read" && len(info.Scopes) == 1 && info.Scopes[0] == "tools:read"
	}
	srv := server.NewServer("auth-test", "1", server.WithToolMiddleware(auth.AuthorizeTools(policy)))
	for _, name := range []string{"read", "write"} {
		srv.RegisterTool(server.Tool{Name: name, Description: name, InputSchema: server.EmptyObjectSchema(), ReadOnlyHint: true, Handler: func(context.Context, map[string]any) (any, error) { calls++; return "executed", nil }})
	}
	provider := auth.NewStaticProvider(map[string]string{"alice-token": "alice", "bob-token": "bob"}).WithScopes("tools:read")
	h := httptransport.Guard(auth.HTTPMiddleware(provider)(httptransport.NewHandler(srv, httptransport.HandlerOptions{JSONResponse: true})), httptransport.HandlerOptions{})
	for _, tt := range []struct {
		token, tool string
		wantError   bool
	}{
		{"alice-token", "read", false}, {"alice-token", "write", true}, {"bob-token", "read", true},
	} {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tt.tool + `","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"}}}}`
		r := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tt.token)
		r.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		r.Header.Set("Mcp-Method", "tools/call")
		r.Header.Set("Mcp-Name", tt.tool)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("X-Forwarded-User-Id", "alice")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var res struct {
			Result struct {
				IsError           bool           `json:"isError"`
				StructuredContent map[string]any `json:"structuredContent"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.Error != nil {
			t.Fatalf("%s/%s: %d %s", tt.token, tt.tool, w.Code, w.Body.String())
		}
		if res.Result.IsError != tt.wantError {
			t.Fatalf("%s/%s: %s", tt.token, tt.tool, w.Body.String())
		}
		if tt.wantError && res.Result.StructuredContent["code"] != "forbidden" {
			t.Fatalf("unexpected denial: %s", w.Body.String())
		}
	}
	if calls != 1 || policyCalls != 3 {
		t.Fatalf("handler calls=%d policy calls=%d", calls, policyCalls)
	}
	// Direct calls have no verified HTTP identity. Supplying an identity in
	// arguments cannot substitute for authentication.
	_, err := srv.CallTool(context.Background(), "read", map[string]any{"userID": "alice"})
	var denied *budget.ToolError
	if !errors.As(err, &denied) || denied.Code != "forbidden" || calls != 1 || policyCalls != 3 {
		t.Fatalf("direct call: %v calls=%d policy calls=%d", err, calls, policyCalls)
	}
}

func TestCanAccessFailsClosed(t *testing.T) {
	def := server.ToolDefinition{Name: "read"}
	if auth.CanAccess(context.Background(), def, func(context.Context, *sdkauth.TokenInfo, server.ToolDefinition) bool {
		t.Fatal("policy received unauthenticated caller")
		return true
	}) {
		t.Fatal("anonymous allowed")
	}
	provider := auth.NewStaticProvider(map[string]string{"test-token": "alice"})
	h := auth.HTTPMiddleware(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.CanAccess(r.Context(), def, nil) {
			t.Fatal("nil policy allowed")
		}
		if !auth.CanAccess(r.Context(), def, func(_ context.Context, info *sdkauth.TokenInfo, got server.ToolDefinition) bool {
			return info.UserID == "alice" && got.Name == "read"
		}) {
			t.Fatal("verified caller denied")
		}
	}))
	r := httptest.NewRequest("GET", "http://localhost/", nil)
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}
