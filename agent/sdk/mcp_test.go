package sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/agent/sdk/internal/fakehost"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/client"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
	httptransport "github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/transport/http"
	"github.com/hollis-labs/libs/mesh/core/gate"
)

func TestMCPToolUsesSharedTransportAndNeverRepeatsUncertainCall(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		name := "success"
		if uncertain {
			name = "uncertain"
		}
		t.Run(name, func(t *testing.T) {
			var effects atomic.Int32
			srv := server.NewServer("synthetic", "0.0.0")
			srv.RegisterTool(server.Tool{Name: "read_value", Description: "Synthetic value", InputSchema: server.EmptyObjectSchema(), ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false, Handler: func(context.Context, map[string]any) (any, error) { effects.Add(1); return "42", nil }})
			base := httptransport.NewHandler(srv, httptransport.HandlerOptions{})
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if uncertain && r.Method == http.MethodPost {
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(raw))
					var message struct {
						Method string `json:"method"`
					}
					_ = json.Unmarshal(raw, &message)
					if message.Method == "tools/call" {
						effects.Add(1)
						http.Error(w, "synthetic transport failure after possible effect", http.StatusServiceUnavailable)
						return
					}
				}
				base.ServeHTTP(w, r)
			})
			ts := httptest.NewServer(handler)
			defer ts.Close()
			pool := client.NewPool(client.WithRetries(3))
			defer pool.Close()
			if err := pool.Register("synthetic", client.ServerConfig{Transport: client.TransportHTTP, URL: ts.URL}); err != nil {
				t.Fatal(err)
			}
			caller, err := pool.Get("synthetic")
			if err != nil {
				t.Fatal(err)
			}
			h := fakehost.New()
			p := h.Ports()
			tool, err := sdk.MCPTool(p.Tools[0].Definition, p.Tools[0].Versions, caller)
			if err != nil {
				t.Fatal(err)
			}
			p.Tools[0] = tool
			o, err := sdk.New(fakehost.Definition(), p)
			if err != nil {
				t.Fatal(err)
			}
			result, err := o.Run(context.Background(), h.Input())
			if uncertain {
				if err == nil || effects.Load() != 1 || h.ModelCalls != 1 || h.Receipts[len(h.Receipts)-1].State != gate.ExecutionUnknown {
					t.Fatalf("uncertain call repeated/flattened: effects=%d models=%d err=%v", effects.Load(), h.ModelCalls, err)
				}
			} else if err != nil || result.Status != sdk.Succeeded || effects.Load() != 1 || h.ModelCalls != 2 {
				t.Fatalf("MCP path failed: %+v effects=%d err=%v", result, effects.Load(), err)
			}
		})
	}
}
