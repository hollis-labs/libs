// Package host assembles app-owned HTTP routes and embedded GUI assets.
package host

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	webui "github.com/hollis-labs/go-webui"
)

type Config struct {
	Name     string
	Assets   fs.FS
	BasePath string
	// Routes and Plugins receive the original URL path, including their prefix.
	Routes          http.Handler
	Plugins         http.Handler
	ShutdownTimeout time.Duration
	// Stop releases app-owned resources after HTTP requests drain.
	Stop func(context.Context) error
}
type Host struct {
	cfg     Config
	handler http.Handler
}

func New(cfg Config) (*Host, error) {
	if strings.TrimSpace(cfg.Name) == "" {
		return nil, fmt.Errorf("host: name is required")
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 5 * time.Second
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	api := cfg.Routes
	if api == nil {
		api = http.NotFoundHandler()
	}
	mux.Handle("/api/", api)
	plugins := cfg.Plugins
	if plugins == nil {
		plugins = http.NotFoundHandler()
	}
	mux.Handle("/plugins/", plugins)
	base := "/" + strings.Trim(cfg.BasePath, "/")
	if base != "/" {
		base += "/"
	}
	mux.Handle(base, webui.Handler(webui.Config{FS: cfg.Assets, BasePath: cfg.BasePath}))
	return &Host{cfg: cfg, handler: mux}, nil
}
func (h *Host) Handler() http.Handler { return h.handler }

// Serve owns listener until cancellation. Cancellation drains HTTP before Stop.
func (h *Host) Serve(ctx context.Context, listener net.Listener) error {
	server := &http.Server{Handler: h.handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	var serveErr error
	select {
	case serveErr = <-finished:
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), h.cfg.ShutdownTimeout)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = server.Close()
	}
	if h.cfg.Stop != nil {
		shutdownErr = errors.Join(shutdownErr, h.cfg.Stop(shutdownCtx))
	}
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, shutdownErr)
}
