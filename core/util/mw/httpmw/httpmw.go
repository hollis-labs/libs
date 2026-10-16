package httpmw

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/libs/core/util/mw"
	"github.com/hollis-labs/libs/core/util/svcerr"
)

// Middleware is the native net/http middleware shape.
type Middleware = func(http.Handler) http.Handler

// Member is an HTTP middleware with its descriptor and mode.
type Member struct {
	Desc mw.Descriptor
	MW   Middleware
	Mode mw.Mode
}

// Stack is a validated HTTP middleware chain.
type Stack struct {
	members []Member
	opts    mw.Options
}

// Assemble validates the members, outermost first, and returns a Stack.
func Assemble(opts mw.Options, members ...Member) (*Stack, error) {
	descs := make([]mw.Descriptor, len(members))
	for i, m := range members {
		if m.MW == nil {
			return nil, fmt.Errorf("member %d (%s): nil middleware", i, m.Desc.Name)
		}
		descs[i] = m.Desc
	}
	if err := mw.Validate(descs, opts); err != nil {
		return nil, err
	}
	return &Stack{members: members, opts: opts}, nil
}

// Then wraps h; the first member is outermost.
func (s *Stack) Then(h http.Handler) http.Handler {
	for i := len(s.members) - 1; i >= 0; i-- {
		m := s.members[i]
		switch m.Mode {
		case mw.Off:
			continue
		case mw.Shadow:
			h = shadow(m, s.opts.Observer)(h)
		default:
			h = enforce(m, s.opts.Observer)(h)
		}
	}
	return h
}

// Explain prints the chain: order, phase, failure mode and mode.
func (s *Stack) Explain() string {
	descs := make([]mw.Descriptor, len(s.members))
	modes := make([]mw.Mode, len(s.members))
	for i, m := range s.members {
		descs[i], modes[i] = m.Desc, m.Mode
	}
	return mw.Explain(descs, modes)
}

func emit(obs func(mw.Event), d mw.Descriptor, mode mw.Mode, o mw.Outcome, err error) {
	if obs != nil {
		obs(mw.Event{Name: d.Name, Phase: d.Phase, Mode: mode, Outcome: o, Err: err})
	}
}

// enforce applies the failure mode to a panic in the middleware itself.
// HTTP middleware cannot return a Fault, so a panic before next is the only
// signal; a panic after next was called belongs to the outer recover.
func enforce(m Member, obs func(mw.Event)) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var called, downPanic bool
			tracked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				defer func() {
					if p := recover(); p != nil {
						downPanic = true
						panic(p)
					}
				}()
				next.ServeHTTP(w, r)
			})
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				if downPanic {
					panic(p) // the handler's panic, not this member's
				}
				emit(obs, m.Desc, mw.Enforce, mw.Faulted, fmt.Errorf("panic: %v", p))
				switch {
				case m.Desc.Failure == mw.Open && called:
					// response already committed by downstream; it stands
				case m.Desc.Failure == mw.Open:
					next.ServeHTTP(w, r)
				case called:
					panic(p) // closed, but the response is committed: outer recover logs it
				default:
					WriteError(w, errInternal)
				}
			}()
			m.MW(tracked).ServeHTTP(w, r)
			if called {
				emit(obs, m.Desc, mw.Enforce, mw.Passed, nil)
			} else {
				emit(obs, m.Desc, mw.Enforce, mw.Denied, nil)
			}
		})
	}
}

const shadowBodyCap = 1 << 20

// shadow runs the middleware against a stub handler and a throwaway writer,
// records whether it would have let the request through, then serves the real
// handler. The request body is buffered (up to 1 MiB) so both sides can read it.
func shadow(m Member, obs func(mw.Event)) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body []byte
			if r.Body != nil && r.Body != http.NoBody {
				body, _ = io.ReadAll(io.LimitReader(r.Body, shadowBodyCap+1))
				r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
			}
			probe := r.Clone(r.Context())
			probe.Body = io.NopCloser(bytes.NewReader(body))
			stubbed := false
			stub := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { stubbed = true })
			rec := &sink{h: http.Header{}, status: http.StatusOK}
			func() {
				defer func() {
					if p := recover(); p != nil {
						emit(obs, m.Desc, mw.Shadow, mw.Faulted, fmt.Errorf("panic: %v", p))
					}
				}()
				m.MW(stub).ServeHTTP(rec, probe)
				if stubbed {
					emit(obs, m.Desc, mw.Shadow, mw.Passed, nil)
				} else {
					emit(obs, m.Desc, mw.Shadow, mw.Denied, fmt.Errorf("would respond %d", rec.status))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type sink struct {
	h      http.Header
	status int
}

func (s *sink) Header() http.Header         { return s.h }
func (s *sink) WriteHeader(c int)           { s.status = c }
func (s *sink) Write(b []byte) (int, error) { return len(b), nil }

// WriteError writes err in the shared svcerr envelope. A message is only ever
// the error's safe text, never an internal cause.
func WriteError(w http.ResponseWriter, err error) {
	svcerr.WriteJSON(w, err, http.StatusInternalServerError)
}

var (
	errInternal = svcerr.New(svcerr.CodeInternal, "internal error")
	errTooLarge = svcerr.New("body_too_large", "request body too large", svcerr.WithStatus(http.StatusRequestEntityTooLarge))
)

// --- standard set ---

// Recover turns a handler panic into a 500.
func Recover(log *slog.Logger) Member {
	return Member{
		Desc: mw.Descriptor{Name: "recover", Phase: mw.PhaseRecover, Failure: mw.Closed},
		MW: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer func() {
					if p := recover(); p != nil {
						if p == http.ErrAbortHandler {
							panic(p)
						}
						log.Error("panic", "panic", fmt.Sprint(p), "request_id", RequestIDFrom(r.Context()))
						WriteError(w, errInternal)
					}
				}()
				next.ServeHTTP(w, r)
			})
		},
	}
}

type ctxKey int

const requestIDKey ctxKey = 1

// RequestIDFrom returns the request ID bound by RequestID, or "".
func RequestIDFrom(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey).(string)
	return s
}

// RequestIDHeader is the header RequestID reads and writes.
const RequestIDHeader = "X-Request-ID"

// RequestID reuses a sane inbound ID or mints one, binds it to the context and
// echoes it on the response. Provides "request_id".
func RequestID() Member {
	return Member{
		Desc: mw.Descriptor{Name: "request-id", Phase: mw.PhaseTrace, Failure: mw.Open, Provides: []string{"request_id"}},
		MW: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := r.Header.Get(RequestIDHeader)
				if !validID(id) {
					var b [12]byte
					_, _ = rand.Read(b[:])
					id = hex.EncodeToString(b[:])
				}
				w.Header().Set(RequestIDHeader, id)
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
			})
		},
	}
}

func validID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

type statusWriter struct {
	http.ResponseWriter
	status, bytes int
}

func (s *statusWriter) WriteHeader(c int) {
	if s.status == 0 {
		s.status = c
	}
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog logs one structured line per request. It never logs headers or
// query strings (they carry tokens).
func AccessLog(log *slog.Logger) Member {
	return Member{
		Desc: mw.Descriptor{Name: "access-log", Phase: mw.PhaseAccessLog, Failure: mw.Open, Requires: []string{"request_id"}},
		MW: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				start := time.Now()
				sw := &statusWriter{ResponseWriter: w}
				defer func() {
					st := sw.status
					if st == 0 {
						st = http.StatusOK
					}
					log.Info("http", "request_id", RequestIDFrom(r.Context()), "method", r.Method,
						"path", r.URL.Path, "status", st, "bytes", sw.bytes, "dur", time.Since(start))
				}()
				next.ServeHTTP(sw, r)
			})
		},
	}
}

// BodyLimit rejects bodies over max: 413 up front when Content-Length says so,
// and a MaxBytesReader for chunked or lying clients.
func BodyLimit(max int64) Member {
	return Member{
		Desc: mw.Descriptor{Name: "body-limit", Phase: mw.PhaseLimits, Failure: mw.Closed},
		MW: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ContentLength > max {
					WriteError(w, errTooLarge)
					return
				}
				r.Body = http.MaxBytesReader(w, r.Body, max)
				next.ServeHTTP(w, r)
			})
		},
	}
}

// Timeout bounds the request context. The handler must honor it.
func Timeout(d time.Duration) Member {
	return Member{
		Desc: mw.Descriptor{Name: "timeout", Phase: mw.PhaseLimits, Failure: mw.Closed},
		MW: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithTimeout(r.Context(), d)
				defer cancel()
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		},
	}
}

// CORSConfig lists exact allowed origins; "*" is not accepted.
type CORSConfig struct {
	Origins []string
	Methods []string
	Headers []string
	MaxAge  time.Duration
}

// CORS answers preflights and tags allowed origins. Unknown origins get no CORS
// headers (the browser enforces); it never rejects server-side.
func CORS(cfg CORSConfig) Member {
	allowed := map[string]bool{}
	for _, o := range cfg.Origins {
		allowed[o] = true
	}
	methods := strings.Join(cfg.Methods, ", ")
	headers := strings.Join(cfg.Headers, ", ")
	return Member{
		Desc: mw.Descriptor{Name: "cors", Phase: mw.PhaseCORS, Failure: mw.Open},
		MW: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				o := r.Header.Get("Origin")
				if o != "" && allowed[o] {
					w.Header().Add("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Origin", o)
					if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
						w.Header().Set("Access-Control-Allow-Methods", methods)
						w.Header().Set("Access-Control-Allow-Headers", headers)
						if cfg.MaxAge > 0 {
							w.Header().Set("Access-Control-Max-Age", strconv.Itoa(int(cfg.MaxAge.Seconds())))
						}
						w.WriteHeader(http.StatusNoContent)
						return
					}
				}
				next.ServeHTTP(w, r)
			})
		},
	}
}
