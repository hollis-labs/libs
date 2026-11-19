package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

const resourceWellKnown = "/.well-known/oauth-protected-resource"

// ProtectedResourceMetadata is the discovery configuration for one MCP endpoint
// (RFC 9728). AuthorizationServers contains issuer URLs, never JWKS URLs. The
// issuer must provide its own authorization-server metadata and token service;
// publishing this document does not implement an OAuth provider or validate
// token audience. All fields are public, and must contain no credentials.
type ProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported,omitempty"`
	ResourceName         string   `json:"resource_name,omitempty"`
}

// ProtectedResource serves a snapshot of validated discovery configuration.
// Construct one per externally visible MCP endpoint; URLs are configured, never
// inferred from Host or forwarded headers.
type ProtectedResource struct {
	body         []byte
	metadataURL  string
	metadataPath string
}

// NewProtectedResource validates RFC 9728 metadata for the MCP authorization
// discovery requirements (2026-07-28). Resource and authorization-server URLs
// must use HTTPS, without credentials, query or fragment. Local plain-HTTP
// development can still use HTTPMiddleware without discovery.
func NewProtectedResource(metadata ProtectedResourceMetadata) (*ProtectedResource, error) {
	resource, err := discoveryURL(metadata.Resource)
	if err != nil {
		return nil, fmt.Errorf("auth: invalid resource URL: %w", err)
	}
	if len(metadata.AuthorizationServers) == 0 {
		return nil, errors.New("auth: at least one authorization server is required")
	}
	for _, issuer := range metadata.AuthorizationServers {
		if _, err := discoveryURL(issuer); err != nil {
			return nil, fmt.Errorf("auth: invalid authorization server URL: %w", err)
		}
	}
	for _, scope := range metadata.ScopesSupported {
		if scope == "" {
			return nil, errors.New("auth: empty supported scope")
		}
		for _, c := range scope {
			if c < 0x21 || c > 0x7e || c == '"' || c == '\\' {
				return nil, errors.New("auth: invalid supported scope")
			}
		}
	}
	body, err := json.Marshal(struct {
		ProtectedResourceMetadata
		BearerMethodsSupported []string `json:"bearer_methods_supported"`
	}{metadata, []string{"header"}})
	if err != nil {
		return nil, err
	}
	// RFC 9728 §3.1: remove the terminating slash after the authority before
	// inserting the well-known prefix. Preserve all other path bytes.
	path := resource.EscapedPath()
	if path == "/" {
		path = ""
	}
	metadataPath := resourceWellKnown + path
	return &ProtectedResource{body: body, metadataPath: metadataPath,
		metadataURL: resource.Scheme + "://" + resource.Host + metadataPath}, nil
}

func discoveryURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") ||
		u.Opaque != "" || strings.ContainsAny(u.Host, "\\ \t\r\n") {
		return nil, errors.New("expected an absolute HTTPS URL without credentials, query or fragment")
	}
	if strings.Contains(u.Hostname(), ":") {
		if net.ParseIP(u.Hostname()) == nil {
			return nil, errors.New("invalid URL host")
		}
	} else if strings.ContainsAny(u.Host, "[]") {
		return nil, errors.New("invalid URL host")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("invalid URL port")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("invalid URL port")
	}
	return u, nil
}

// MetadataURL is the configured, absolute URL advertised in the challenge.
func (p *ProtectedResource) MetadataURL() string { return p.metadataURL }

// HTTPMiddleware serves public discovery at both the endpoint-specific
// well-known path and the root well-known path, and authenticates all other
// requests through provider. A nil provider panics: discovery must not
// accidentally disable authentication. Provider options (expiration, scopes,
// clock skew) are preserved, with ResourceMetadataURL set to MetadataURL().
//
// Apply transport/http.Guard outside this middleware when serving both MCP and
// discovery routes. For multiple MCP resources on one host, mount their
// endpoint-specific discovery routes explicitly using MetadataHandler, and
// choose the root document in the application rather than nesting this method.
func (p *ProtectedResource) HTTPMiddleware(provider Provider) func(http.Handler) http.Handler {
	if provider == nil {
		panic("auth: protected resource requires a provider")
	}
	opts := sdkauth.RequireBearerTokenOptions{}
	if configured := provider.Options(); configured != nil {
		opts = *configured
	}
	opts.ResourceMetadataURL = p.metadataURL
	enforce := sdkauth.RequireBearerToken(provider.Verifier(), &opts)
	return func(next http.Handler) http.Handler {
		authenticated := enforce(next)
		metadata := p.MetadataHandler()
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.EscapedPath() == p.metadataPath || r.URL.EscapedPath() == resourceWellKnown {
				metadata.ServeHTTP(w, r)
				return
			}
			authenticated.ServeHTTP(w, r)
		})
	}
}

// MetadataHandler serves public metadata independently of authentication.
// Mount it at MetadataURL's path when composing a multi-resource HTTP mux.
// GET, HEAD and public CORS discovery preflight are supported.
func (p *ProtectedResource) MetadataHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		switch r.Method {
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet, http.MethodHead:
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				_, _ = w.Write(p.body)
			}
		default:
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}
