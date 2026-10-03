package federation

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the federation config file. Its shape is Torque's, the reference
// deployment, plus one field: Ops. It is a JSON file rather than environment
// variables because peer lists and certificate paths do not fit the latter.
//
// The exported fields are the file schema. The unexported ones are filled in by
// LoadConfig once the config validates: the loaded identity and the peer registry.
type Config struct {
	// ListenAddr is the bind address of the federation TLS listener, for example
	// ":8443". The listener is separate from any plaintext surface.
	ListenAddr string `json:"listen_addr"`

	// LocalAuthorities are the authorities this install homes. At least one is
	// required: an install cannot tell local from foreign without declaring what
	// it homes.
	LocalAuthorities []string `json:"local_authorities"`

	// Identity is the path pair for this install's certificate and private key,
	// presented as the client certificate when dialing and as the server
	// certificate when accepting.
	Identity IdentityConfig `json:"identity"`

	// Peers is the inbound peer registry: who may connect, the fingerprints that
	// identify them, and the authorities each may act for.
	Peers []PeerConfig `json:"peers"`

	// ForeignRoutes is the outbound registry: the foreign authority, the endpoint
	// that homes it, and the pinned fingerprints of that endpoint's certificate.
	ForeignRoutes []RouteConfig `json:"foreign_routes"`

	// Ops names the operations to expose, for example
	// ["send","get","thread","consume","cancel"]. Absent means DefaultOpSet
	// (Inbox and Subscribe off). Listing "inbox" or "subscribe" is the deliberate
	// widening the default withholds: see DefaultOpSet.
	Ops []Op `json:"ops,omitempty"`

	sourceDir string
	identity  tls.Certificate
	peers     *PeerRegistry
	opSet     OpSet
}

// IdentityConfig is the path pair for this install's identity. A relative path
// is resolved against the directory holding the config file.
type IdentityConfig struct {
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

// RouteConfig is one outbound foreign route.
type RouteConfig struct {
	Authority  string   `json:"authority"`
	Endpoint   string   `json:"endpoint"`
	ServerPins []string `json:"server_pins"`
}

// LoadConfig reads and validates the config at path.
//
//   - No file: (nil, nil). Federation is disabled, and nothing runs: no listener,
//     no routes, no attack surface. This is the standalone guarantee.
//   - A valid file: (*Config, nil).
//   - A malformed or invalid file: (nil, error). Federation is a security
//     boundary, so a broken config fails loudly instead of degrading to an open or
//     silently disabled state. Unknown keys are errors: a typo in a security
//     config must not become a default.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the operator named this file
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("federation: read config %s: %w", path, err)
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("federation: parse config %s: %w", path, err)
	}
	c.sourceDir = filepath.Dir(path)
	if err := c.finalize(); err != nil {
		return nil, fmt.Errorf("federation: invalid config %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) finalize() error {
	if c.ListenAddr == "" {
		return errors.New("listen_addr is required")
	}
	if len(c.LocalAuthorities) == 0 {
		return errors.New("local_authorities must list at least one authority this install homes")
	}
	local := make(map[string]struct{}, len(c.LocalAuthorities))
	for _, a := range c.LocalAuthorities {
		if a == "" {
			return errors.New("local_authorities entries must be non-empty")
		}
		if _, dup := local[a]; dup {
			return fmt.Errorf("local authority %q is listed twice", a)
		}
		local[a] = struct{}{}
	}

	if c.Identity.CertFile == "" || c.Identity.KeyFile == "" {
		return errors.New("identity.cert_file and identity.key_file are both required")
	}
	cert, err := tls.LoadX509KeyPair(c.resolve(c.Identity.CertFile), c.resolve(c.Identity.KeyFile))
	if err != nil {
		return fmt.Errorf("load identity key pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse identity certificate: %w", err)
	}
	cert.Leaf = leaf
	c.identity = cert

	peers, err := NewPeerRegistry(c.Peers)
	if err != nil {
		return err
	}
	c.peers = peers

	seen := make(map[string]struct{}, len(c.ForeignRoutes))
	for _, rt := range c.ForeignRoutes {
		if rt.Authority == "" {
			return errors.New("a foreign route has an empty authority")
		}
		if _, dup := seen[rt.Authority]; dup {
			return fmt.Errorf("foreign route authority %q is declared twice", rt.Authority)
		}
		seen[rt.Authority] = struct{}{}
		if _, clash := local[rt.Authority]; clash {
			return fmt.Errorf("authority %q is both a local authority and a foreign route", rt.Authority)
		}
		if err := validateEndpoint(rt.Endpoint); err != nil {
			return fmt.Errorf("foreign route %q: %w", rt.Authority, err)
		}
		if len(rt.ServerPins) == 0 {
			return fmt.Errorf("foreign route %q: at least one server_pins fingerprint is required (the hop is pinned)", rt.Authority)
		}
		for _, pin := range rt.ServerPins {
			if _, err := ValidateFingerprint(pin); err != nil {
				return fmt.Errorf("foreign route %q: %w", rt.Authority, err)
			}
		}
	}

	if c.Ops == nil {
		c.opSet = DefaultOpSet()
	} else {
		set, err := NewOpSet(c.Ops...)
		if err != nil {
			return fmt.Errorf("ops: %w", err)
		}
		if set, err = set.validate(); err != nil {
			return fmt.Errorf("ops: %w", err)
		}
		c.opSet = set
	}
	return nil
}

func (c *Config) resolve(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(c.sourceDir, path)
}

// OpSet is the operation set the config selects.
func (c *Config) OpSet() OpSet { return c.opSet.Clone() }
