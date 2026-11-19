package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

func TestProtectedResourceDiscovery(t *testing.T) {
	config := ProtectedResourceMetadata{Resource: "https://mcp.example/public/mcp", AuthorizationServers: []string{"https://issuer.example/tenant"}, ScopesSupported: []string{"tools:read"}}
	resource, err := NewProtectedResource(config)
	if err != nil {
		t.Fatal(err)
	}
	if resource.MetadataURL() != "https://mcp.example/.well-known/oauth-protected-resource/public/mcp" {
		t.Fatal(resource.MetadataURL())
	}
	// Discovery owns a snapshot, including slices supplied by the caller.
	config.AuthorizationServers[0] = "https://changed.example"
	provider := NewStaticProvider(map[string]string{"test-token": "alice"}).WithScopes("tools:read")
	h := resource.HTTPMiddleware(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := sdkauth.TokenInfoFromContext(r.Context()); got == nil || got.UserID != "alice" {
			t.Errorf("verified identity missing: %v", got)
		}
		w.WriteHeader(204)
	}))
	for _, path := range []string{"/.well-known/oauth-protected-resource/public/mcp", "/.well-known/oauth-protected-resource"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "https://mcp.example"+path, nil))
		var document map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &document) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if document["resource"] != config.Resource || document["authorization_servers"].([]any)[0] != "https://issuer.example/tenant" || document["bearer_methods_supported"].([]any)[0] != "header" {
			t.Fatalf("metadata: %v", document)
		}
		if w.Header().Get("WWW-Authenticate") != "" || w.Header().Get("Content-Type") != "application/json" {
			t.Fatal(w.Header())
		}
	}
	for _, token := range []string{"", "wrong-token", "test-token"} {
		r := httptest.NewRequest("POST", "https://mcp.example/public/mcp", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		// A request authority must never influence the configured challenge URL.
		r.Host = "attacker.example"
		r.Header.Set("X-Forwarded-Host", "attacker.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if token == "test-token" {
			if w.Code != 204 {
				t.Fatal(w.Code)
			}
			continue
		}
		if w.Code != 401 {
			t.Fatalf("status=%d", w.Code)
		}
		challenge := w.Header().Get("WWW-Authenticate")
		if challenge != `Bearer resource_metadata="https://mcp.example/.well-known/oauth-protected-resource/public/mcp", scope="tools:read"` {
			t.Fatalf("challenge=%s", challenge)
		}
	}
	if provider.Options().ResourceMetadataURL != "" {
		t.Fatal("provider options mutated")
	}
	for _, method := range []string{"HEAD", "OPTIONS", "POST"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "https://mcp.example/.well-known/oauth-protected-resource", nil))
		want := 200
		if method == "OPTIONS" {
			want = 204
		}
		if method == "POST" {
			want = 405
		}
		if w.Code != want {
			t.Fatalf("%s status=%d", method, w.Code)
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
	}
}

func TestProtectedResourceValidation(t *testing.T) {
	valid := ProtectedResourceMetadata{Resource: "https://mcp.example/mcp", AuthorizationServers: []string{"https://issuer.example"}}
	for _, raw := range []string{"", "http://mcp.example/mcp", "https:///mcp", "https://user@mcp.example/mcp", "https://mcp.example/mcp#fragment", "https://mcp.example/mcp#", "https://mcp.example/mcp?query=1", "https://mcp.example/mcp?", "https://mcp.example:bad/mcp", "https://mcp.example:99999/mcp", "https://[invalid]/mcp", "https://mcp.example:/mcp"} {
		tt := valid
		tt.Resource = raw
		if _, err := NewProtectedResource(tt); err == nil {
			t.Errorf("accepted resource %q", raw)
		}
	}
	for _, issuers := range [][]string{nil, {}, {""}, {"http://issuer.example"}, {"https://issuer.example?x=1"}, {"https://issuer.example#x"}, {"https://issuer.example", "invalid"}} {
		tt := valid
		tt.AuthorizationServers = issuers
		if _, err := NewProtectedResource(tt); err == nil {
			t.Errorf("accepted issuers %v", issuers)
		}
	}
	for _, scope := range []string{"", "read write", "read\nwrite", `read"write`, `read\write`} {
		tt := valid
		tt.ScopesSupported = []string{scope}
		if _, err := NewProtectedResource(tt); err == nil {
			t.Errorf("accepted scope %q", scope)
		}
	}
	for resource, want := range map[string]string{"https://mcp.example": "https://mcp.example/.well-known/oauth-protected-resource", "https://mcp.example/": "https://mcp.example/.well-known/oauth-protected-resource", "https://mcp.example/a%2Fb": "https://mcp.example/.well-known/oauth-protected-resource/a%2Fb"} {
		tt := valid
		tt.Resource = resource
		p, err := NewProtectedResource(tt)
		if err != nil || p.MetadataURL() != want {
			t.Fatalf("%s: %v %v", resource, p, err)
		}
	}
}

type testProvider struct {
	opts *sdkauth.RequireBearerTokenOptions
	info *sdkauth.TokenInfo
}

func (p testProvider) Options() *sdkauth.RequireBearerTokenOptions { return p.opts }
func (p testProvider) Verifier() sdkauth.TokenVerifier {
	return func(context.Context, string, *http.Request) (*sdkauth.TokenInfo, error) { return p.info, nil }
}

func TestProtectedResourcePreservesTokenValidation(t *testing.T) {
	resource, err := NewProtectedResource(ProtectedResourceMetadata{Resource: "https://mcp.example", AuthorizationServers: []string{"https://issuer.example"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		info *sdkauth.TokenInfo
		opts *sdkauth.RequireBearerTokenOptions
		want int
	}{
		{"expired", &sdkauth.TokenInfo{Expiration: time.Now().Add(-time.Hour)}, nil, 401},
		{"missing expiration", &sdkauth.TokenInfo{}, nil, 401},
		{"missing scope", &sdkauth.TokenInfo{Expiration: time.Now().Add(time.Hour)}, &sdkauth.RequireBearerTokenOptions{Scopes: []string{"write"}}, 403},
		{"clock skew", &sdkauth.TokenInfo{Expiration: time.Now().Add(-time.Minute)}, &sdkauth.RequireBearerTokenOptions{ClockSkew: time.Hour}, 204},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := resource.HTTPMiddleware(testProvider{tt.opts, tt.info})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
			r := httptest.NewRequest("POST", "https://mcp.example", nil)
			r.Header.Set("Authorization", "Bearer test-token")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tt.want == 401 && !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata=") {
				t.Fatal("missing challenge")
			}
		})
	}
}
