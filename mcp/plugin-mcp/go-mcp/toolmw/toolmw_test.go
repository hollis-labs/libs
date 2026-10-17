package toolmw_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/util/mw"
	"github.com/hollis-labs/libs/core/util/mw/conformance"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/budget"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/toolmw"
)

// Tool is the conformance harness for this adapter.
type harness struct{}

func (harness) Name() string            { return "mcp-tool" }
func (harness) CommittedResponse() bool { return false }

func (harness) Run(probes []conformance.Probe, deny bool) (conformance.Result, error) {
	var res conformance.Result
	members := []toolmw.Member{toolmw.Recover()}
	for _, p := range probes {
		members = append(members, toolmw.Member{
			Desc: p.Desc, Mode: p.Mode,
			Build: func(server.ToolDefinition) toolmw.Middleware {
				return func(next toolmw.Handler) toolmw.Handler {
					return func(ctx context.Context, a toolmw.Args) (any, error) {
						res.Trace = append(res.Trace, p.Desc.Name+">")
						defer func() { res.Trace = append(res.Trace, "<"+p.Desc.Name) }()
						switch p.Does {
						case conformance.DenyIfAsked:
							if a["deny"] == true {
								return nil, toolmw.Denied("denied")
							}
						case conformance.PanicBefore:
							panic("probe")
						case conformance.PanicAfter:
							_, _ = next(ctx, a)
							panic("probe")
						}
						return next(ctx, a)
					}
				}
			},
		})
	}
	s, err := toolmw.Assemble(mw.Options{Observer: conformance.Collect(&res.Events)}, members...)
	if err != nil {
		return res, err
	}
	h := s.Register(server.ToolDefinition{Name: "probe"}, func(context.Context, toolmw.Args) (any, error) { res.HandlerCalls++; return "ok", nil })
	_, err = h(context.Background(), toolmw.Args{"deny": deny})
	res.Failed = err != nil
	return res, nil
}

func TestConformance(t *testing.T) { conformance.Run(t, harness{}) }

func echo(_ context.Context, a toolmw.Args) (any, error) { return a, nil }

func TestBuildRunsOncePerTool(t *testing.T) {
	var builds, calls int32
	m := toolmw.Member{
		Desc: mw.Descriptor{Name: "counting", Phase: mw.PhaseTrace, Failure: mw.Open},
		Build: func(server.ToolDefinition) toolmw.Middleware {
			atomic.AddInt32(&builds, 1)
			return func(next toolmw.Handler) toolmw.Handler {
				return func(ctx context.Context, a toolmw.Args) (any, error) {
					atomic.AddInt32(&calls, 1)
					return next(ctx, a)
				}
			}
		},
	}
	s, _ := toolmw.Assemble(mw.Options{}, m)
	h := s.Register(server.ToolDefinition{Name: "a"}, echo)
	s.Register(server.ToolDefinition{Name: "b"}, echo)
	for i := 0; i < 5; i++ {
		h(context.Background(), nil)
	}
	if builds != 2 || calls != 5 {
		t.Fatalf("builds=%d calls=%d", builds, calls)
	}
}

// Through a real go-mcp server: WithToolMiddleware + RegisterChecked + CallTool.
func TestThroughRealServer(t *testing.T) {
	stack, err := toolmw.Assemble(mw.Options{},
		toolmw.Recover(),
		toolmw.Timeout(20*time.Millisecond, map[string]time.Duration{"slow_ok": time.Second}),
		toolmw.Wrap(mw.Descriptor{Name: "strict-args", Phase: mw.PhaseLimits, Failure: mw.Closed}, server.StrictArgs()),
		toolmw.DenyDestructive(),
	)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.NewServer("t", "0", server.WithToolMiddleware(stack.Middleware()))
	schema := server.ObjectSchema(map[string]any{"q": map[string]any{"type": "string"}})
	wait := func(ctx context.Context, _ map[string]any) (any, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(60 * time.Millisecond):
			return "done", nil
		}
	}
	srv.RegisterChecked(server.Tool{Name: "slow", Description: "d", InputSchema: schema, Handler: wait}, server.Reads("test"))
	srv.RegisterChecked(server.Tool{Name: "slow_ok", Description: "d", InputSchema: schema, Handler: wait}, server.Reads("test"))
	srv.RegisterChecked(server.Tool{Name: "boom", Description: "d", InputSchema: schema,
		Handler: func(context.Context, map[string]any) (any, error) { panic("secret internals") }}, server.Writes())
	srv.RegisterChecked(server.Tool{Name: "drop", Description: "d", InputSchema: schema, Handler: wait}, server.Destroys("test"))

	ctx := context.Background()
	var te *budget.ToolError

	if _, err := srv.CallTool(ctx, "slow", nil); !errors.As(err, &te) || te.Code != "timeout" {
		t.Fatalf("default timeout: %v", err)
	}
	if out, err := srv.CallTool(ctx, "slow_ok", nil); err != nil || out != "done" {
		t.Fatalf("per-tool timeout: %v %v", out, err)
	}
	_, err = srv.CallTool(ctx, "boom", nil)
	if !errors.As(err, &te) || te.Code != "internal" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("panic must become a safe internal error: %v", err)
	}
	if _, err = srv.CallTool(ctx, "drop", nil); !errors.As(err, &te) || te.Code != "permission_denied" {
		t.Fatalf("destructive: %v", err)
	}
	if _, err = srv.CallTool(ctx, "slow_ok", map[string]any{"nope": 1}); !errors.As(err, &te) || te.Code != "invalid_argument" {
		t.Fatalf("go-mcp StrictArgs through Wrap: %v", err)
	}
	if !strings.Contains(stack.Explain(server.ToolDefinition{Name: "drop"}), "deny-destructive") {
		t.Fatal("explain")
	}
}
