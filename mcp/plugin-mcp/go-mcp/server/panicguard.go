package server

import (
	"context"
	"errors"

	"github.com/hollis-labs/libs/core/panics"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/budget"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// CodeInternalError is the budget.ToolError code a recovered tool panic is
// reported under.
const CodeInternalError = "internal_error"

// panicGuardDefault is whether NewServer installs the panic guard when the
// caller says nothing. It is true: the guard only turns a process crash into
// an error result and mutates no data, unlike sanitization. WithoutPanicGuard
// opts out.
const panicGuardDefault = true

// WithPanicGuard sets the guard that recovers panics in tool handlers and in
// receiving-middleware-dispatched requests. nil means panics.Default(). It
// also enables the guard if WithoutPanicGuard was given earlier.
func WithPanicGuard(g *panics.Guard) Option {
	return func(o *options) {
		o.panicGuard = true
		o.panicGuardSet = g
	}
}

// WithoutPanicGuard turns the default panic guard off, for a caller that
// recovers panics itself.
func WithoutPanicGuard() Option {
	return func(o *options) { o.panicGuard = false }
}

func (o *options) guard() *panics.Guard {
	if o.panicGuardSet != nil {
		return o.panicGuardSet
	}
	return panics.Default()
}

// RecoverPanics returns tool middleware that recovers a panic in the wrapped
// handler. The caller gets a structured budget.ToolError (code
// "internal_error") that carries the panic ref and nothing from the panic; the
// value and stack go to g's Reporter, redacted. The next call is served as
// usual. g nil means panics.Default(), resolved per call.
//
// NewServer installs this as the outermost tool middleware unless
// WithoutPanicGuard is given; use this function directly to place it elsewhere.
func RecoverPanics(g *panics.Guard) ToolMiddleware {
	return func(def ToolDefinition, next ToolHandler) ToolHandler {
		where := "mcp tool " + def.Name
		return func(ctx context.Context, args map[string]any) (res any, err error) {
			gd := g
			if gd == nil {
				gd = panics.Default()
			}
			perr := gd.Call(where, func() (e error) {
				res, e = next(ctx, args)
				return e
			})
			var pe *panics.Error
			if asPanic(perr, &pe) {
				te := budget.NewToolError(CodeInternalError, pe.Error())
				te.NextStep = "report this ref to the operator; retrying the same call will likely fail the same way"
				return nil, te
			}
			return res, perr
		}
	}
}

// RecoverPanicsReceiving is the receiving-middleware form, for requests that
// are not tools/call (resources, prompts, completion). A panic becomes a
// JSON-RPC internal error carrying the ref.
func RecoverPanicsReceiving(g *panics.Guard) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (res mcpsdk.Result, err error) {
			gd := g
			if gd == nil {
				gd = panics.Default()
			}
			perr := gd.Call("mcp "+method, func() (e error) {
				res, err = next(ctx, method, req)
				return nil
			})
			if perr != nil {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: perr.Error()}
			}
			return res, err
		}
	}
}

func asPanic(err error, target **panics.Error) bool {
	return errors.As(err, target)
}
