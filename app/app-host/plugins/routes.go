package plugins

import (
	"io"
	"net/http"
	"strings"

	pluginhost "github.com/hollis-labs/libs/plugin-mcp/plugin-host"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

// MaxRouteBody bounds buffered request bodies forwarded to a plugin.
const MaxRouteBody = 4 << 20

// Client returns the running plugin's RPC client by inventory ID. It is nil
// until Start succeeds and after Stop.
func (i *Inventory) Client(id string) (*pluginhost.Client, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, p := range i.processes {
		if p.Info().ID == id {
			return p.Client(), true
		}
	}
	return nil, false
}

// APIRoutes forwards GET/POST/etc. under /api/plugins/{id}/ to the plugin's
// http/handle. The plugin receives the full original path and decides what it
// serves; the app still owns auth, so mount this behind its own policy.
// Responses are buffered: streaming (SSE) is not available on this path.
func (i *Inventory) APIRoutes() http.Handler {
	const prefix = "/api/plugins/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, prefix)
		id, _, _ := strings.Cut(rest, "/")
		if !ok || id == "" {
			http.NotFound(w, r)
			return
		}
		client, ok := i.Client(id)
		if !ok {
			http.Error(w, "plugin not running", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, MaxRouteBody+1))
		if err != nil || len(body) > MaxRouteBody {
			http.Error(w, "bad or oversized body", http.StatusRequestEntityTooLarge)
			return
		}
		req := subprocess.HTTPRequest{Method: r.Method, Path: r.URL.Path, Body: body,
			Query: map[string]string{}, Headers: map[string]string{}}
		for k, v := range r.URL.Query() {
			req.Query[k] = v[0]
		}
		// Only a content type is forwarded; credentials stay with the host.
		if ct := r.Header.Get("Content-Type"); ct != "" {
			req.Headers["Content-Type"] = ct
		}
		resp, err := client.HTTPHandle(r.Context(), req)
		if err != nil {
			http.Error(w, "plugin error", http.StatusBadGateway)
			return
		}
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		if resp.Status == 0 {
			resp.Status = http.StatusOK
		}
		w.WriteHeader(resp.Status)
		_, _ = w.Write(resp.Body)
	})
}
