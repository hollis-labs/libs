package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
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
	// OnRequest, optional, is called by Middleware (not AuthorizeTool) for every
	// guarded request, after the decision and before the handler or the error
	// response, with the request, the principal that will be stamped (zero when
	// none) and the audit event. It lets an app attach request-scoped audit
	// without re-parsing the request. Audit, if set, is still called.
	OnRequest func(*http.Request, Principal, Event)
	// Addr, optional, is the listen address. When set, SetMode refuses a mode
	// that ValidateBind would refuse for it, so a live reload cannot loosen
	// auth on a non-local listener.
	Addr string
	// ReloadBind, optional, is the listener a RELOAD is judged against. When
	// set, Apply, SetMode and CheckApply refuse to move to a mode weaker than
	// the current one (enforce -> permissive or off, permissive -> off) unless
	// IsLoopbackAddr(ReloadBind): on any other listener, tailnet included, a
	// reload may keep or tighten the mode but never loosen it. Unlike Addr it
	// does not judge a mode that is not weaker, so a process started on a
	// token-gated off/permissive listener can still reload its verifier. It
	// never applies to New (the startup mode is the caller's, and Addr/
	// ValidateBind is the startup rule). A loosening that is allowed is logged
	// at WARN naming the old and new mode.
	ReloadBind string
}

// ErrLoosenRefused is returned (wrapped) by Apply, SetMode and CheckApply when
// Config.ReloadBind names a listener that is not loopback and the new mode is
// weaker than the current one.
var ErrLoosenRefused = errors.New("auth: a reload may not loosen the mode")

// Guard applies the mode logic to HTTP requests and MCP tool calls.
type Guard struct {
	cfg Config
	lg  *slog.Logger
	st  atomic.Pointer[guardState] // live mode and verifier; see Apply
}

// pinKey carries, in a request context, the guardState Middleware admitted the
// request under. It is per Guard so one Guard's pin never answers for another.
type pinKey struct{ g *Guard }

// guardState is what a request is decided under. It is replaced as a whole,
// and a request loads it once on entry, so the mode and the verifier it uses
// always belong together and never change mid-request.
type guardState struct {
	mode     Mode
	verifier Verifier
}

// New validates cfg. An empty Mode is an error: callers choose explicitly
// (see ModeFromEnv).
func New(cfg Config) (*Guard, error) {
	mode, err := ParseMode(string(cfg.Mode)) // canonical form: "ENFORCE" must not be stored as-is
	if err != nil {
		return nil, err
	}
	cfg.Mode = mode
	if mode == ModeEnforce && cfg.Verifier == nil {
		return nil, errors.New("auth: enforce mode needs a Verifier")
	}
	g := &Guard{cfg: cfg, lg: cfg.Logger}
	g.st.Store(&guardState{mode: mode, verifier: cfg.Verifier})
	if g.lg == nil {
		g.lg = slog.Default()
	}
	if mode != ModeEnforce {
		g.lg.Warn("auth is not enforcing", "auth", string(mode))
	}
	return g, nil
}

// Mode returns the current mode.
func (g *Guard) Mode() Mode { return g.st.Load().mode }

// SetMode changes the mode of a running Guard, so a posture dial can move
// without a restart. It is Apply(m, nil): see Apply for what it refuses.
func (g *Guard) SetMode(m Mode) error { return g.Apply(m, nil) }

// CheckApply reports whether Apply(m, v) would succeed, without changing
// anything. A reload checks first, then builds the rest of its configuration.
func (g *Guard) CheckApply(m Mode, v Verifier) error {
	_, _, err := g.next(m, v)
	return err
}

// Apply replaces the Guard's mode and, if v is not nil, its verifier, as one
// atomic step: a request sees the old pair or the new pair, never a mix, and
// each request keeps the pair it started under. It refuses, leaving both
// unchanged, a mode ParseMode does not accept (the canonical form is stored,
// so "ENFORCE" and " observe " work), enforce with no verifier, and (when
// Config.Addr is set) a mode ValidateBind rejects for that address.
func (g *Guard) Apply(m Mode, v Verifier) error {
	for {
		cur := g.st.Load()
		mode, st, err := g.nextFrom(cur, m, v)
		if err != nil {
			return err
		}
		if g.st.CompareAndSwap(cur, st) {
			if cur.mode != mode {
				g.lg.Warn("auth mode changed", "from", string(cur.mode), "to", string(mode))
			}
			return nil
		}
	}
}

func (g *Guard) next(m Mode, v Verifier) (Mode, *guardState, error) {
	return g.nextFrom(g.st.Load(), m, v)
}

func (g *Guard) nextFrom(cur *guardState, m Mode, v Verifier) (Mode, *guardState, error) {
	mode, err := ParseMode(string(m))
	if err != nil {
		return "", nil, err
	}
	if v == nil {
		v = cur.verifier
	}
	if mode == ModeEnforce && v == nil {
		return "", nil, errors.New("auth: enforce mode needs a Verifier")
	}
	if g.cfg.Addr != "" {
		if err := ValidateBind(g.cfg.Addr, mode); err != nil {
			return "", nil, err
		}
	}
	if g.cfg.ReloadBind != "" && mode.WeakerThan(cur.mode) && !IsLoopbackAddr(g.cfg.ReloadBind) {
		return "", nil, fmt.Errorf("%w: %s -> %s on a listener that is not loopback (restart to change it)", ErrLoosenRefused, cur.mode, mode)
	}
	return mode, &guardState{mode: mode, verifier: v}, nil
}

// Status is the string for health/whoami: "auth=<mode>".
func (g *Guard) Status() string { return "auth=" + string(g.Mode()) }

func (g *Guard) authenticate(ctx context.Context, v Verifier, headers []string) (Principal, string) {
	tok, st := ParseBearer(headers)
	if st != "" {
		return Principal{}, st
	}
	return g.verify(ctx, v, tok)
}

func (g *Guard) verify(ctx context.Context, v Verifier, tok string) (Principal, string) {
	if v == nil {
		return Principal{}, StateUnavailable
	}
	p, err := v.Verify(ctx, tok)
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
func (g *Guard) decision(mode Mode, p Principal, state, required string, unmapped bool, ev *Event) (Principal, bool, int, string) {
	stamp := state == StateVerified
	deny, status, code, reason := false, 0, "", ""
	switch {
	case state == StateUnavailable:
		deny, status, code, reason = true, http.StatusServiceUnavailable, "auth_unavailable", "verifier unavailable"
	case state != StateVerified:
		deny, status, code, reason = true, http.StatusUnauthorized, "unauthorized", "authentication "+state
	case unmapped:
		deny, status, code, reason = true, http.StatusForbidden, "route_not_mapped", "route or tool has no scope mapping"
	case required != "" && !p.HasScope(required):
		deny, status, code, reason = true, http.StatusForbidden, "scope_required", "missing scope "+required
	}
	if mode == ModeOff && state != StateVerified {
		p, stamp = Principal{ID: OffModePrincipalID, Kind: KindOperator, Display: "operator (auth off)", Scopes: []string{"*"}}, true
		ev.Authentication = StateOffMode
	}
	ev.RequiredScope = required
	if state == StateVerified {
		ev.Verified, ev.PrincipalID, ev.SessionID = true, p.ID, p.SessionID
	} else if stamp {
		ev.PrincipalID = p.ID
	}
	switch mode {
	case ModePermissive:
		if deny {
			ev.WouldDeny, ev.Reason = true, reason
			g.lg.Info("auth: would deny", "required_scope", required, "reason", reason, "route", ev.Route, "tool", ev.Tool)
		}
	case ModeOff:
		if deny && state == StateVerified { // token present but under-scoped: attribute, don't reject
			ev.WouldDeny, ev.Reason = true, reason
		}
	default: // ModeEnforce, and any mode this switch does not know: fail closed
		if deny {
			ev.Denied, ev.Reason = true, reason
			return Principal{}, false, status, code
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
		cur := g.st.Load() // one mode and verifier for the whole request, taken before any blocking work
		// Pin the generation in the request context so a later AuthorizeTool
		// call from the handler is judged by the same mode the request was
		// admitted under, not by whatever Apply installed meanwhile.
		r = r.WithContext(context.WithValue(r.Context(), pinKey{g}, cur))
		p, state := g.authenticate(r.Context(), cur.verifier, r.Header.Values("Authorization"))
		required, known := g.cfg.Scopes.ForRoute(r.Method, r.URL.Path)
		unmapped := g.cfg.Scopes.denyUnmapped(known, required)
		mode := cur.mode
		ev := Event{At: time.Now().UTC(), Mode: mode, Authentication: state, Method: r.Method, Route: routeFamily(r.URL.Path)}
		p, stamp, status, code := g.decision(mode, p, state, required, unmapped, &ev)
		g.emit(ev)
		if g.cfg.OnRequest != nil {
			g.cfg.OnRequest(r, p, ev)
		}
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
	required, known := g.cfg.Scopes.ForTool(tool)
	unmapped := g.cfg.Scopes.denyUnmapped(known, required)
	p, ok := FromContext(ctx)
	// AuthorizeTool verifies nothing itself: the principal was stamped by
	// Middleware. Follow the generation Middleware pinned for this request;
	// with none (a stdio transport that stamped the principal itself) the
	// current generation applies.
	mode := g.Mode()
	if pinned, has := ctx.Value(pinKey{g}).(*guardState); has {
		mode = pinned.mode
	}
	state := StateMissing
	if ok && p.Verified {
		state = StateVerified
	} else if ok && mode == ModeOff {
		state = StateVerified // synthetic off-mode principal
		p.Verified = true
	}
	ev := Event{At: time.Now().UTC(), Mode: mode, Authentication: state, Tool: tool}
	_, _, status, _ := g.decision(mode, p, state, required, unmapped, &ev)
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
