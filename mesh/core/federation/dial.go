package federation

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"time"

	gomsg "github.com/hollis-labs/go-messaging"
	"github.com/hollis-labs/go-messaging/httpstore"
)

// Profile returns the httpstore dialect Dial speaks: Torque's federation hop
// (BasePath, a JSON Consume body, repeated filter parameters), with the
// operations ops leaves off marked unsupported so the client fails them without
// a request. Inbox and Subscribe are carried in the same shapes Tether's routes
// use, under BasePath, and only when ops turns them on.
func Profile(ops OpSet) httpstore.Profile {
	p := httpstore.TorqueFederationProfile()
	p.Name = "federation"
	p.Unsupported = nil
	names := map[Op]httpstore.Op{
		OpSend: httpstore.OpSend, OpGet: httpstore.OpGet, OpThread: httpstore.OpThread, OpConsume: httpstore.OpConsume,
		OpCancel: httpstore.OpCancel, OpInbox: httpstore.OpInbox, OpSubscribe: httpstore.OpSubscribe,
	}
	for _, o := range allOps {
		if !ops.Allows(o) {
			p.Unsupported = append(p.Unsupported, names[o])
		}
	}
	// The request/reply helper subscribes for the response, so it exists only if
	// Subscribe does; this package's Store does not add a Dispatcher.
	return p
}

// DialOption configures Dial.
type DialOption func(*dialConfig)

type dialConfig struct {
	identity tls.Certificate
	pins     []string
	ops      OpSet
	timeout  time.Duration
	extra    []httpstore.Option
}

// WithIdentity sets the certificate this install presents to the peer. Required.
func WithIdentity(cert tls.Certificate) DialOption { return func(c *dialConfig) { c.identity = cert } }

// WithServerPins sets the SHA-256 fingerprints the peer's server certificate must
// match. Required: an unpinned route is refused.
func WithServerPins(pins ...string) DialOption {
	return func(c *dialConfig) { c.pins = append(c.pins, pins...) }
}

// WithOpSet sets which operations the returned Store will call (default
// DefaultOpSet). The others fail client-side without a request.
func WithOpSet(ops OpSet) DialOption { return func(c *dialConfig) { c.ops = ops } }

// WithTimeout bounds each non-streaming call (httpstore's default otherwise).
func WithTimeout(d time.Duration) DialOption { return func(c *dialConfig) { c.timeout = d } }

// WithHTTPStoreOptions passes further options to httpstore.New, such as a request
// hook for trace context. WithProfile and WithHTTPClient given here are overridden:
// the profile and the pinned mTLS client are what make this a federation Store.
func WithHTTPStoreOptions(opts ...httpstore.Option) DialOption {
	return func(c *dialConfig) { c.extra = append(c.extra, opts...) }
}

// Dial returns a go-messaging Store that reaches the peer at endpoint over mutual
// TLS: this install's certificate presented and the peer's pinned. It composes
// httpstore rather than reimplementing an HTTP Store, and is what a
// go-messaging.Router registers as the route for a foreign authority.
//
// endpoint must be https with a host; identity and at least one server pin are
// required. Every misconfiguration fails here, not on first use.
func Dial(endpoint string, opts ...DialOption) (gomsg.Store, error) {
	var cfg dialConfig
	for _, o := range opts {
		o(&cfg)
	}
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	ops, err := cfg.ops.validate()
	if err != nil {
		return nil, err
	}
	client, err := ClientHTTPClient(cfg.identity, cfg.pins)
	if err != nil {
		return nil, err
	}
	hopts := append([]httpstore.Option(nil), cfg.extra...)
	hopts = append(hopts, httpstore.WithProfile(Profile(ops)), httpstore.WithHTTPClient(client))
	if cfg.timeout > 0 {
		hopts = append(hopts, httpstore.WithTimeout(cfg.timeout))
	}
	return httpstore.New(endpoint, hopts...)
}

// validateEndpoint checks a foreign-route endpoint is an absolute https URL. The
// hop is mutual TLS, so a plaintext endpoint cannot carry it.
func validateEndpoint(endpoint string) error {
	if endpoint == "" {
		return errors.New("federation: endpoint is required")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("federation: endpoint %q: %w", endpoint, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("federation: endpoint %q: scheme must be https (the federation hop is mutual TLS)", endpoint)
	}
	if u.Host == "" {
		return fmt.Errorf("federation: endpoint %q: missing host", endpoint)
	}
	return nil
}
