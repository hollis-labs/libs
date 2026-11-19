package httptransport

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	gmcp "github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// HandlerOptions configures the HTTP handler.
type HandlerOptions struct {
	// AllowedHosts lists trusted HTTP authorities (hostname or IP, optionally
	// with a port). An entry without a port permits any port. Empty defaults
	// to localhost and loopback IP literals only. No DNS resolution, wildcards,
	// forwarded headers, or implicit public hosts are trusted.
	AllowedHosts []string

	// AllowedOrigins lists exact http(s) origins. Empty permits only same-origin
	// requests (scheme and authority), after Host validation. Requests without
	// Origin remain allowed. "null", malformed, and multiple origins are rejected.
	AllowedOrigins []string

	// JSONResponse makes the handler answer a POST with a single
	// application/json body instead of a text/event-stream (SSE) frame, even
	// when the client sends Accept: application/json, text/event-stream.
	// Default false keeps the SSE framing.
	JSONResponse bool
}

// NewHandler returns an http.Handler exposing server over the MCP
// Streamable HTTP transport.
func NewHandler(server *gmcp.Server, opts HandlerOptions) http.Handler {
	sdkHandler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return server.SDKServer() },
		&mcpsdk.StreamableHTTPOptions{
			Stateless: true, JSONResponse: opts.JSONResponse,
			// An explicit allowlist supersedes the SDK's local-address heuristic
			// (e.g. a trusted reverse proxy connecting to a loopback listener).
			// Guard still validates every Host before the SDK receives it.
			DisableLocalhostProtection: len(opts.AllowedHosts) > 0,
		},
	)

	return Guard(sdkHandler, opts)
}

// Guard applies the same Host and Origin policy as NewHandler to an entire
// HTTP stack, including public discovery routes. Put it outside authentication
// middleware so untrusted authorities are rejected before token verification.
func Guard(next http.Handler, opts HandlerOptions) http.Handler {
	hosts := make(map[string]struct{}, len(opts.AllowedHosts))
	for _, host := range opts.AllowedHosts {
		if authority, _, ok := parseAuthority(host); ok {
			hosts[authority] = struct{}{}
		}
	}
	origins := make(map[string]struct{}, len(opts.AllowedOrigins))
	for _, origin := range opts.AllowedOrigins {
		if canonical, ok := parseOrigin(origin); ok {
			origins[canonical] = struct{}{}
		}
	}
	return &originGuard{hosts: hosts, origins: origins,
		defaultHosts:   len(opts.AllowedHosts) == 0,
		defaultOrigins: len(opts.AllowedOrigins) == 0, next: next}
}

type originGuard struct {
	hosts, origins               map[string]struct{}
	defaultHosts, defaultOrigins bool
	next                         http.Handler
}

func (g *originGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	authority, hostname, ok := parseAuthority(r.Host)
	if !ok || !g.hostAllowed(authority, hostname) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	values := r.Header.Values("Origin")
	if len(values) > 1 {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if len(values) == 1 {
		origin, valid := parseOrigin(values[0])
		_, allowed := g.origins[origin]
		if g.defaultOrigins {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			same, _ := parseOrigin(scheme + "://" + authority)
			allowed = origin == same
		}
		if !valid || !allowed {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
	}
	g.next.ServeHTTP(w, r)
}

func (g *originGuard) hostAllowed(authority, hostname string) bool {
	if g.defaultHosts {
		return hostname == "localhost" || net.ParseIP(hostname).IsLoopback()
	}
	if _, ok := g.hosts[authority]; ok {
		return true
	}
	// A hostname-only entry permits any port; IPv6 entries use brackets.
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	_, ok := g.hosts[hostname]
	return ok
}

// parseAuthority deliberately accepts no URL syntax, trailing dot, userinfo,
// zone identifier or ambiguous unbracketed IPv6. Ports must be numeric.
func parseAuthority(raw string) (authority, hostname string, ok bool) {
	if raw == "" || strings.ContainsAny(raw, "/\\?#@% 	\r\n") {
		return "", "", false
	}
	u, err := url.Parse("http://" + raw)
	if err != nil || u.Host != raw || u.Hostname() == "" {
		return "", "", false
	}
	hostname = strings.ToLower(u.Hostname())
	if strings.HasSuffix(hostname, ".") {
		return "", "", false
	}
	if strings.Contains(hostname, ":") {
		if net.ParseIP(hostname) == nil || !strings.HasPrefix(raw, "[") {
			return "", "", false
		}
	} else {
		if strings.ContainsAny(raw, "[]") {
			return "", "", false
		}
		for _, c := range hostname {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
				return "", "", false
			}
		}
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", "", false
		}
	} else if strings.HasSuffix(raw, ":") {
		return "", "", false
	}
	return strings.ToLower(raw), hostname, true
}

func parseOrigin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.Contains(raw, "#") {
		return "", false
	}
	authority, _, ok := parseAuthority(u.Host)
	if !ok {
		return "", false
	}
	// Default ports are equivalent for same-origin checks.
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		authority = strings.TrimSuffix(authority, ":"+u.Port())
	}
	return u.Scheme + "://" + authority, true
}
