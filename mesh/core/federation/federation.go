package federation

import (
	"context"
	"fmt"

	gomsg "github.com/hollis-labs/substrate/mesh/messaging"
)

// Federation is a configured federation subsystem: the inbound mutual-TLS
// surface, and the outbound Router that reaches the peers.
type Federation struct {
	cfg    *Config
	local  gomsg.Store
	server *Server
	store  gomsg.Store
}

// Enable loads the config at path and, if there is one, builds the subsystem
// against local, the install's own Store.
//
//   - No config file: (nil, nil): federation is off. The caller treats a nil
//     *Federation as standalone, with no listener, no foreign routes and no extra
//     configuration.
//   - A valid file: (*Federation, nil).
//   - An invalid file: (nil, error).
func Enable(path string, local gomsg.Store, opts ...ServerOption) (*Federation, error) {
	cfg, err := LoadConfig(path)
	if err != nil || cfg == nil {
		return nil, err
	}
	return New(cfg, local, opts...)
}

// New builds the subsystem from an already loaded Config.
func New(cfg *Config, local gomsg.Store, opts ...ServerOption) (*Federation, error) {
	if cfg == nil {
		return nil, fmt.Errorf("federation: New needs a Config")
	}
	server, err := NewServer(local, MTLSPinnedResolver(cfg.peers), cfg.opSet, cfg.LocalAuthorities, opts...)
	if err != nil {
		return nil, err
	}
	f := &Federation{cfg: cfg, local: local, server: server}
	if f.store, err = f.buildStore(); err != nil {
		return nil, err
	}
	return f, nil
}

// buildStore composes go-messaging's Router: the local Store serves the local
// authorities and each foreign route is a Dial to the peer that homes it. The
// Router does the authority dispatch; nothing here reimplements it.
//
// A Router knows one local authority. With exactly one, its own strict mode
// rejects an unknown authority. With several, the Router is built without one
// and a guard rejects any address whose authority is neither local nor routed, so
// the strictness does not depend on how many authorities the install homes.
func (f *Federation) buildStore() (gomsg.Store, error) {
	multi := len(f.cfg.LocalAuthorities) > 1
	var r *gomsg.Router
	if multi {
		r = gomsg.NewRouter(f.local, "")
	} else {
		r = gomsg.NewRouter(f.local, f.cfg.LocalAuthorities[0], gomsg.WithStrictRouting())
	}
	for _, rt := range f.cfg.ForeignRoutes {
		remote, err := Dial(rt.Endpoint, WithIdentity(f.cfg.identity), WithServerPins(rt.ServerPins...), WithOpSet(f.cfg.opSet))
		if err != nil {
			return nil, fmt.Errorf("federation: foreign route %q: %w", rt.Authority, err)
		}
		if err := r.Register(rt.Authority, remote); err != nil {
			return nil, fmt.Errorf("federation: foreign route %q: %w", rt.Authority, err)
		}
	}
	if !multi {
		return r, nil
	}
	return &authorityGuard{Store: r, router: r, local: toSet(f.cfg.LocalAuthorities)}, nil
}

// Store is the install's messaging Store with federation applied: an address in
// a foreign authority is routed to its peer over mutual TLS, a local one is served
// locally, and any other is refused with go-messaging's ErrNoRoute.
func (f *Federation) Store() gomsg.Store { return f.store }

// Server is the inbound surface, for embedding in a listener of your own.
func (f *Federation) Server() *Server { return f.server }

// ListenAddr is the bind address of the federation listener.
func (f *Federation) ListenAddr() string { return f.cfg.ListenAddr }

// LocalAuthorities are the authorities this install homes.
func (f *Federation) LocalAuthorities() []string {
	return append([]string(nil), f.cfg.LocalAuthorities...)
}

// Run serves the federation listener until ctx is canceled.
func (f *Federation) Run(ctx context.Context) error {
	tc, err := ServerTLSConfig(f.cfg.identity, f.cfg.peers)
	if err != nil {
		return err
	}
	return f.server.Run(ctx, f.cfg.ListenAddr, tc)
}

func toSet(in []string) map[string]struct{} {
	m := make(map[string]struct{}, len(in))
	for _, a := range in {
		m[a] = struct{}{}
	}
	return m
}

// authorityGuard refuses an address whose authority is neither local nor routed,
// for a Router built without a single local authority. It adds no dispatch: every
// permitted call goes to the Router.
type authorityGuard struct {
	gomsg.Store
	router *gomsg.Router
	local  map[string]struct{}
}

func (g *authorityGuard) known(a gomsg.Address) error {
	if _, ok := g.local[a.Authority]; ok || !g.router.IsLocal(a.Authority) {
		return nil
	}
	return fmt.Errorf("%w: %q", gomsg.ErrNoRoute, a.Authority)
}

func (g *authorityGuard) Send(ctx context.Context, env gomsg.Envelope) (gomsg.Envelope, error) {
	if err := g.known(env.To); err != nil {
		return gomsg.Envelope{}, err
	}
	return g.Store.Send(ctx, env)
}

func (g *authorityGuard) Inbox(ctx context.Context, to gomsg.Address, f gomsg.Filter) ([]gomsg.Envelope, error) {
	if err := g.known(to); err != nil {
		return nil, err
	}
	return g.Store.Inbox(ctx, to, f)
}

func (g *authorityGuard) Consume(ctx context.Context, id string, recipient gomsg.Address) error {
	if err := g.known(recipient); err != nil {
		return err
	}
	return g.Store.Consume(ctx, id, recipient)
}

func (g *authorityGuard) Subscribe(ctx context.Context, to gomsg.Address, f gomsg.Filter) (<-chan gomsg.Envelope, error) {
	if err := g.known(to); err != nil {
		return nil, err
	}
	return g.Store.Subscribe(ctx, to, f)
}
