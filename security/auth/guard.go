package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Event is the audit record for one request or tool call. It carries metadata
// only: never bearer headers, query strings or bodies.
type Event struct {
	At             time.Time `json:"at"`
	Mode           Mode      `json:"mode"`
	Authentication string    `json:"authentication"` // Authentication state constants
	Verified       bool      `json:"verified"`
	PrincipalID    string    `json:"principal_id,omitempty"`
	SessionID      string    `json:"session_id,omitempty"`
	Method         string    `json:"method,omitempty"`
	Route          string    `json:"route,omitempty"` // first path segment only
	Tool           string    `json:"tool,omitempty"`
	RequiredScope  string    `json:"required_scope,omitempty"`
	WouldDeny      bool      `json:"would_deny,omitempty"` // permissive/off: enforce would have rejected
	Denied         bool      `json:"denied,omitempty"`     // enforce rejected
	Reason         string    `json:"reason,omitempty"`
}

// Config configures a Guard.
type Config struct {
	Mode     Mode
	Verifier Verifier
	Scopes   *ScopeTable       // optional route/tool to scope table
	Audit    func(Event)       // optional; called for every guarded request
	Logger   *slog.Logger      // default slog.Default()
	OpenPath func(string) bool // paths that skip auth; default only "/health"
}

// Guard applies the mode logic to HTTP requests and MCP tool calls.
type Guard struct {
	cfg Config
	lg  *slog.Logger
}

// New validates cfg. An empty Mode is an error: callers choose explicitly
// (see ModeFromEnv).
func New(cfg Config) (*Guard, error) {
	if _, err := ParseMode(string(cfg.Mode)); err != nil {
		return nil, err
	}
	if cfg.Mode == ModeEnforce && cfg.Verifier == nil {
		return nil, errors.New("auth: enforce mode needs a Verifier")
	}
	g := &Guard{cfg: cfg, lg: cfg.Logger}
	if g.lg == nil {
		g.lg = slog.Default()
	}
	if cfg.Mode != ModeEnforce {
		g.lg.Warn("auth is not enforcing", "auth", string(cfg.Mode))
	}
	return g, nil
}

// Mode returns the configured mode.
func (g *Guard) Mode() Mode { return g.cfg.Mode }

// Status is the string for health/whoami: "auth=<mode>".
func (g *Guard) Status() string { return "auth=" + string(g.cfg.Mode) }

func (g *Guard) authenticate(ctx context.Context, headers []string) (Principal, string) {
	tok, st := ParseBearer(headers)
	if st != "" {
		return Principal{}, st
	}
	return g.verify(ctx, tok)
}

func (g *Guard) verify(ctx context.Context, tok string) (Principal, string) {
	if g.cfg.Verifier == nil {
		return Principal{}, StateUnavailable
	}
	p, err := g.cfg.Verifier.Verify(ctx, tok)
	switch {
	case err == nil:
		p.Verified = true
		return p, StateVerified
	case errors.Is(err, ErrInvalidToken):
		return Principal{}, StateInvalid
	default:
		return Principal{}, StateUnavailable
	}
}

// decision applies the mode to an authentication result and required scope.
// It returns the principal to stamp (zero, false when none), and the HTTP
// status and code if enforce rejects (status 0 = allow).
func (g *Guard) decision(p Principal, state, required string, ev *Event) (Principal, bool, int, string) {
	stamp := state == StateVerified
	deny, status, code, reason := false, 0, "", ""
	switch {
	case state == StateUnavailable:
		deny, status, code, reason = true, http.StatusServiceUnavailable, "auth_unavailable", "verifier unavailable"
	case state != StateVerified:
		deny, status, code, reason = true, http.StatusUnauthorized, "unauthorized", "authentication "+state
	case required != "" && !p.HasScope(required):
		deny, status, code, reason = true, http.StatusForbidden, "scope_required", "missing scope "+required
	}
	if g.cfg.Mode == ModeOff && state != StateVerified {
		p, stamp = Principal{ID: OffModePrincipalID, Kind: KindOperator, Display: "operator (auth off)", Scopes: []string{"*"}}, true
		ev.Authentication = StateOffMode
	}
	ev.RequiredScope = required
	if state == StateVerified {
		ev.Verified, ev.PrincipalID, ev.SessionID = true, p.ID, p.SessionID
	} else if stamp {
		ev.PrincipalID = p.ID
	}
	switch g.cfg.Mode {
	case ModeEnforce:
		if deny {
			ev.Denied, ev.Reason = true, reason
			return Principal{}, false, status, code
		}
	case ModePermissive:
		if deny {
			ev.WouldDeny, ev.Reason = true, reason
			g.lg.Info("auth: would deny", "required_scope", required, "reason", reason, "route", ev.Route, "tool", ev.Tool)
		}
	case ModeOff:
		if deny && state == StateVerified { // token present but under-scoped: attribute, don't reject
			ev.WouldDeny, ev.Reason = true, reason
		}
	}
	return p, stamp, 0, ""
}

func (g *Guard) emit(ev Event) {
	if g.cfg.Audit != nil {
		g.cfg.Audit(ev)
	}
}

// Middleware wraps next. Verified (or off-mode synthetic) principals are
// stamped into the request context; read them with FromContext.
func (g *Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (g.cfg.OpenPath == nil && r.URL.Path == "/health") || (g.cfg.OpenPath != nil && g.cfg.OpenPath(r.URL.Path)) {
			next.ServeHTTP(w, r)
			return
		}
		p, state := g.authenticate(r.Context(), r.Header.Values("Authorization"))
		required, _ := g.cfg.Scopes.ForRoute(r.Method, r.URL.Path)
		ev := Event{At: time.Now().UTC(), Mode: g.cfg.Mode, Authentication: state, Method: r.Method, Route: routeFamily(r.URL.Path)}
		p, stamp, status, code := g.decision(p, state, required, &ev)
		g.emit(ev)
		if status != 0 {
			writeError(w, status, code, required)
			return
		}
		if stamp {
			r = r.WithContext(WithPrincipal(r.Context(), p))
		}
		next.ServeHTTP(w, r)
	})
}

// AuthorizeTool checks an MCP tool call against the scope table using the
// principal already stamped in ctx by Middleware (or by a stdio transport via
// WithPrincipal). In enforce mode it returns ErrUnauthenticated or an error
// wrapping ErrForbidden; in permissive and off mode it logs and returns nil.
func (g *Guard) AuthorizeTool(ctx context.Context, tool string) error {
	required, _ := g.cfg.Scopes.ForTool(tool)
	p, ok := FromContext(ctx)
	state := StateMissing
	if ok && p.Verified {
		state = StateVerified
	} else if ok && g.cfg.Mode == ModeOff {
		state = StateVerified // synthetic off-mode principal
		p.Verified = true
	}
	ev := Event{At: time.Now().UTC(), Mode: g.cfg.Mode, Authentication: state, Tool: tool}
	_, _, status, _ := g.decision(p, state, required, &ev)
	g.emit(ev)
	switch status {
	case 0:
		return nil
	case http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrForbidden, required)
	default:
		return ErrUnauthenticated
	}
}

func routeFamily(path string) string {
	first, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if len(first) > 64 {
		return "/<redacted>"
	}
	return "/" + first
}

func writeError(w http.ResponseWriter, status int, code, required string) {
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.WriteHeader(status)
	msg := "verified caller identity required"
	if code == "scope_required" {
		msg = "required scope: " + required
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
