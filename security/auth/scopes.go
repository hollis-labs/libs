package auth

import "strings"

// ScopeTable maps HTTP routes and MCP tool names to the scope they require.
// Scope names are "<app>.<verb>". Build it once at startup; it is read-only
// afterwards and safe for concurrent use.
type ScopeTable struct {
	routes []route
	tools  map[string]string
	// Default is required for routes absent from the table. Empty means
	// authentication alone suffices for unmapped routes.
	Default string
}

type route struct {
	method string
	segs   []string
	scope  string
}

// NewScopeTable returns an empty table.
func NewScopeTable() *ScopeTable { return &ScopeTable{tools: map[string]string{}} }

// Route requires scope for method ("" or "*" = any) and pattern. Pattern
// segments are literal, "{name}" (one segment) or a final "*" (rest of path).
// The first matching route wins, so register specific patterns first.
func (t *ScopeTable) Route(method, pattern, scope string) *ScopeTable {
	if method == "*" {
		method = ""
	}
	t.routes = append(t.routes, route{strings.ToUpper(method), split(pattern), scope})
	return t
}

// Tool requires scope for the named MCP tool.
func (t *ScopeTable) Tool(name, scope string) *ScopeTable {
	t.tools[name] = scope
	return t
}

// ForRoute returns the required scope for a request and whether the route is mapped.
func (t *ScopeTable) ForRoute(method, path string) (scope string, known bool) {
	if t == nil {
		return "", false
	}
	segs := split(path)
	for _, r := range t.routes {
		if (r.method == "" || r.method == strings.ToUpper(method)) && match(r.segs, segs) {
			return r.scope, true
		}
	}
	return t.Default, false
}

// ForTool returns the required scope for an MCP tool and whether it is mapped.
func (t *ScopeTable) ForTool(name string) (scope string, known bool) {
	if t == nil {
		return "", false
	}
	if s, ok := t.tools[name]; ok {
		return s, true
	}
	return t.Default, false
}

func split(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func match(pat, got []string) bool {
	for i, s := range pat {
		if s == "*" && i == len(pat)-1 {
			return true
		}
		if i >= len(got) {
			return false
		}
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			continue
		}
		if s != got[i] {
			return false
		}
	}
	return len(pat) == len(got)
}
