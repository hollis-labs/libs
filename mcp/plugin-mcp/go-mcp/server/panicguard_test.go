package server

import (
	"context"
	"encoding/json"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/libs/core/panics"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/budget"
)

const leakyValue = "sk-live-0123456789abcdefSECRET"

type recorder struct {
	mu   sync.Mutex
	recs []panics.Record
}

func (r *recorder) Report(rec panics.Record) {
	r.mu.Lock()
	r.recs = append(r.recs, rec)
	r.mu.Unlock()
}

func panicServer(t *testing.T, rep *recorder, opts ...Option) *Server {
	t.Helper()
	g := panics.New(panics.Config{Reporter: rep})
	srv := NewServer("kit", "test", append([]Option{WithPanicGuard(g)}, opts...)...)
	srv.RegisterTool(Tool{Name: "boom", InputSchema: EmptyObjectSchema(), Handler: func(context.Context, map[string]any) (any, error) {
		panic("token " + leakyValue)
	}})
	srv.RegisterTool(Tool{Name: "ok", InputSchema: EmptyObjectSchema(), Handler: func(context.Context, map[string]any) (any, error) {
		return "fine", nil
	}})
	return srv
}

// A panicking tool is an error result with a ref, the panic text stays out of
// it, the report has it, and the next call is served.
func TestPanicGuardToolPanicBecomesRefError(t *testing.T) {
	rep := &recorder{}
	srv := panicServer(t, rep)
	cs := connect(t, srv, nil)

	res := callArgs(t, cs, "boom", nil)
	if !res.IsError {
		t.Fatalf("IsError = false: %q", resultText(res))
	}
	txt := resultText(res)
	if strings.Contains(txt, leakyValue) || strings.Contains(txt, "token") {
		t.Fatalf("panic text reached the caller: %s", txt)
	}
	var te budget.ToolError
	if err := json.Unmarshal([]byte(txt), &te); err != nil {
		t.Fatalf("not a structured ToolError: %v (%s)", err, txt)
	}
	if te.Code != CodeInternalError || !strings.Contains(te.Message, "ref pnc_") || te.NextStep == "" {
		t.Fatalf("ToolError = %+v", te)
	}
	if len(rep.recs) != 1 || !strings.Contains(rep.recs[0].Where, "mcp tool boom") || !strings.Contains(te.Message, rep.recs[0].Ref) {
		t.Fatalf("report = %+v", rep.recs)
	}
	if strings.Contains(rep.recs[0].Value, leakyValue) {
		t.Fatalf("reported value not withheld/redacted: %q", rep.recs[0].Value)
	}

	if res := callArgs(t, cs, "ok", nil); res.IsError || resultText(res) != "fine" {
		t.Fatalf("server did not keep serving: %v %q", res.IsError, resultText(res))
	}
}

// CallTool (the non-wire path) is covered too.
func TestPanicGuardCoversCallTool(t *testing.T) {
	srv := panicServer(t, &recorder{})
	_, err := srv.CallTool(context.Background(), "boom", nil)
	if _, ok := panics.Ref(err); !ok {
		// the middleware returns a ToolError whose message carries the ref
		if err == nil || !strings.Contains(err.Error(), "ref pnc_") {
			t.Fatalf("CallTool err = %v", err)
		}
	}
}

// Without the guard the same handler is not recovered, proving the tests above
// are about the middleware.
func TestWithoutPanicGuardDoesNotRecover(t *testing.T) {
	srv := NewServer("kit", "test", WithoutPanicGuard())
	srv.RegisterTool(Tool{Name: "boom", InputSchema: EmptyObjectSchema(), Handler: func(context.Context, map[string]any) (any, error) {
		panic("x")
	}})
	defer func() {
		if recover() == nil {
			t.Fatal("expected the panic to propagate with WithoutPanicGuard")
		}
	}()
	_, _ = srv.CallTool(context.Background(), "boom", nil)
}

// Non-tool requests are guarded by the receiving middleware.
func TestPanicGuardReceivingMiddleware(t *testing.T) {
	rep := &recorder{}
	// A panic in a caller receiving middleware sits inside the guard.
	srv2 := NewServer("kit", "test", WithPanicGuard(panics.New(panics.Config{Reporter: rep})),
		WithReceivingMiddleware(panicOnList))
	cs := connect(t, srv2, nil)
	if _, err := cs.ListTools(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "ref pnc_") || strings.Contains(err.Error(), leakyValue) {
		t.Fatalf("ListTools err = %v", err)
	}
	if _, err := cs.ListPrompts(context.Background(), nil); err != nil {
		t.Fatalf("server did not keep serving: %v", err)
	}
}

func panicOnList(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		if method == "tools/list" {
			panic("token " + leakyValue)
		}
		return next(ctx, method, req)
	}
}
