package toolmw

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hollis-labs/libs/core/util/mw"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/budget"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
)

// Args is a tool call's decoded arguments.
type Args = map[string]any

type (
	// Handler runs one tool call; it has go-mcp's ToolHandler signature.
	Handler = mw.Handler[Args, any]
	// Middleware wraps a tool Handler.
	Middleware = mw.Middleware[Args, any]
)

// Member is a tool middleware with its descriptor. Build runs once per tool.
type Member struct {
	Desc mw.Descriptor
	// Applies limits the member to some tools; nil means all.
	Applies func(server.ToolDefinition) bool
	Build   func(server.ToolDefinition) Middleware
	Mode    mw.Mode
}

// Wrap adapts an existing go-mcp ToolMiddleware (StrictArgs, ValidateSchema).
func Wrap(desc mw.Descriptor, tm server.ToolMiddleware) Member {
	return Member{Desc: desc, Build: func(def server.ToolDefinition) Middleware {
		return func(next Handler) Handler {
			return Handler(tm(def, server.ToolHandler(next)))
		}
	}}
}

// Stack is a validated set of tool middleware.
type Stack struct {
	members []Member
	opts    mw.Options
}

// Assemble validates members, outermost first.
func Assemble(opts mw.Options, members ...Member) (*Stack, error) {
	descs := make([]mw.Descriptor, len(members))
	for i, m := range members {
		if m.Build == nil {
			return nil, fmt.Errorf("member %d (%s): nil Build", i, m.Desc.Name)
		}
		descs[i] = m.Desc
	}
	if err := mw.Validate(descs, opts); err != nil {
		return nil, err
	}
	return &Stack{members: members, opts: opts}, nil
}

// Register wraps h for one tool.
func (s *Stack) Register(def server.ToolDefinition, h Handler) Handler {
	var ms []mw.Member[Args, any]
	for _, m := range s.members {
		if m.Applies != nil && !m.Applies(def) {
			continue
		}
		ms = append(ms, mw.Member[Args, any]{Desc: m.Desc, MW: m.Build(def), Mode: m.Mode})
	}
	st, err := mw.Assemble(s.opts, ms...) // a subset of a valid chain is valid
	if err != nil {
		panic("toolmw: " + err.Error())
	}
	return st.Wrap(h)
}

// Middleware is the stack as one go-mcp ToolMiddleware.
func (s *Stack) Middleware() server.ToolMiddleware {
	return func(def server.ToolDefinition, next server.ToolHandler) server.ToolHandler {
		h := s.Register(def, Handler(next))
		return func(ctx context.Context, a map[string]any) (any, error) {
			out, err := h(ctx, a)
			return out, safe(err)
		}
	}
}

// Explain lists what applies to one tool.
func (s *Stack) Explain(def server.ToolDefinition) string {
	var descs []mw.Descriptor
	var modes []mw.Mode
	for _, m := range s.members {
		if m.Applies == nil || m.Applies(def) {
			descs = append(descs, m.Desc)
			modes = append(modes, m.Mode)
		}
	}
	return "tool " + def.Name + "\n" + mw.Explain(descs, modes)
}

func safe(err error) error {
	var te *budget.ToolError
	switch {
	case err == nil, errors.As(err, &te):
		return err
	case mw.AsFault(err) != nil, errors.Is(err, mw.ErrPanic):
		return budget.NewToolError("internal", "internal error").WithRetryable(true)
	case errors.Is(err, context.DeadlineExceeded):
		return budget.NewToolError("timeout", "the tool did not finish in time").WithRetryable(true)
	}
	return err
}

// --- standard set ---

// Recover turns a panic below it into an error wrapping mw.ErrPanic.
func Recover() Member {
	return Member{
		Desc: mw.Descriptor{Name: "recover", Phase: mw.PhaseRecover, Failure: mw.Closed},
		Build: func(server.ToolDefinition) Middleware {
			return func(next Handler) Handler {
				return func(ctx context.Context, a Args) (out any, err error) {
					defer func() {
						if p := recover(); p != nil {
							err = fmt.Errorf("%w: %v", mw.ErrPanic, p)
						}
					}()
					return next(ctx, a)
				}
			}
		},
	}
}

// Audit logs tool, duration and outcome; never arguments or results.
func Audit(log *slog.Logger) Member {
	return Member{
		Desc: mw.Descriptor{Name: "audit", Phase: mw.PhaseAccessLog, Failure: mw.Open},
		Build: func(def server.ToolDefinition) Middleware {
			return func(next Handler) Handler {
				return func(ctx context.Context, a Args) (any, error) {
					start := time.Now()
					out, err := next(ctx, a)
					outcome := "ok"
					var te *budget.ToolError
					switch {
					case errors.As(err, &te) && te.Code == "permission_denied":
						outcome = "denied"
					case err != nil:
						outcome = "error"
					}
					log.Info("tool", "tool", def.Name, "outcome", outcome, "dur", time.Since(start))
					return out, err
				}
			}
		},
	}
}

// Timeout bounds each call; perTool overrides by tool name, resolved at registration.
func Timeout(d time.Duration, perTool map[string]time.Duration) Member {
	return Member{
		Desc: mw.Descriptor{Name: "timeout", Phase: mw.PhaseLimits, Failure: mw.Closed},
		Build: func(def server.ToolDefinition) Middleware {
			lim := d
			if v, ok := perTool[def.Name]; ok {
				lim = v
			}
			return func(next Handler) Handler {
				return func(ctx context.Context, a Args) (any, error) {
					ctx, cancel := context.WithTimeout(ctx, lim)
					defer cancel()
					return next(ctx, a)
				}
			}
		},
	}
}

// Authorize is the policy seam. check returns nil to allow, or an error to
// refuse (use Denied for a safe message). Closed: a fault refuses the call.
func Authorize(check func(ctx context.Context, def server.ToolDefinition, a Args) error) Member {
	return Member{
		Desc: mw.Descriptor{Name: "authorize", Phase: mw.PhaseAuthz, Failure: mw.Closed},
		Build: func(def server.ToolDefinition) Middleware {
			return func(next Handler) Handler {
				return func(ctx context.Context, a Args) (any, error) {
					if err := check(ctx, def, a); err != nil {
						return nil, err
					}
					return next(ctx, a)
				}
			}
		},
	}
}

// Denied is a refusal whose message is safe to show the caller.
func Denied(msg string) error { return budget.NewToolError("permission_denied", msg) }

// DenyDestructive refuses tools annotated destructive. A stand-in for the
// effect-class gate in the policy design.
func DenyDestructive() Member {
	m := Authorize(func(_ context.Context, def server.ToolDefinition, _ Args) error {
		if def.Annotations.DestructiveHint {
			return Denied("tool " + def.Name + " is not permitted")
		}
		return nil
	})
	m.Desc.Name = "deny-destructive"
	return m
}
