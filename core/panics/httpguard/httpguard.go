// Package httpguard contains panics in net/http handlers. net/http itself
// survives a handler panic, but it drops the connection without a response and
// records nothing; this middleware answers with a 500 that carries a ref and
// reports the panic through the panics package.
package httpguard

import (
	"encoding/json"
	"net/http"

	"github.com/hollis-labs/libs/core/panics"
)

// Option configures [Handler].
type Option func(*config)

type config struct {
	guard   *panics.Guard
	respond func(w http.ResponseWriter, r *http.Request, pe *panics.Error)
}

// WithGuard uses g instead of the default [panics.Guard].
func WithGuard(g *panics.Guard) Option { return func(c *config) { c.guard = g } }

// WithResponder replaces the default JSON error body, for an application with
// its own error envelope. The responder must not include anything from the panic
// beyond pe.Error() and pe.Rec.Ref.
func WithResponder(f func(w http.ResponseWriter, r *http.Request, pe *panics.Error)) Option {
	return func(c *config) { c.respond = f }
}

// Handler wraps next so a panic in it becomes a 500 with a ref. A handler that
// panics with [http.ErrAbortHandler] keeps its meaning and is re-panicked. If the
// handler had already started the response, the status can no longer change; the
// panic is still reported and the connection is closed by net/http as usual.
func Handler(next http.Handler, opts ...Option) http.Handler {
	c := &config{respond: defaultResponder}
	for _, o := range opts {
		o(c)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &trackingWriter{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
				panic(v)
			}
			g := c.guard
			if g == nil {
				g = panics.Default()
			}
			route := r.Pattern // the matched pattern, never the raw path (it may carry ids or secrets)
			if route == "" {
				route = "unmatched"
			}
			pe := g.Capture("http "+route, v)
			if !tw.wrote {
				c.respond(w, r, pe)
			}
		}()
		next.ServeHTTP(tw, r)
	})
}

func defaultResponder(w http.ResponseWriter, _ *http.Request, pe *panics.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": "internal", "message": pe.Error(), "ref": pe.Rec.Ref,
	}})
}

// trackingWriter records whether the response has started, and keeps the
// optional interfaces working (http.ResponseController uses Unwrap; Flush is
// forwarded for code that type-asserts http.Flusher).
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackingWriter) WriteHeader(code int) { t.wrote = true; t.ResponseWriter.WriteHeader(code) }
func (t *trackingWriter) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}
func (t *trackingWriter) Flush() {
	t.wrote = true
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (t *trackingWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }
