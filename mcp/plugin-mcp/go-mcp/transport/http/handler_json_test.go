package httptransport

import (
	"net/http/httptest"
	"strings"
	"testing"

	gmcp "github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
)

func TestJSONResponseOption(t *testing.T) {
	const body = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	for _, tc := range []struct {
		json bool
		want string
	}{{false, "text/event-stream"}, {true, "application/json"}} {
		h := NewHandler(gmcp.NewServer("t", "0"), HandlerOptions{JSONResponse: tc.json})
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.want) {
			t.Errorf("JSONResponse=%v: Content-Type %q, want %q (body %s)", tc.json, ct, tc.want, w.Body)
		}
	}
}
