package tether

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const EnvironmentProtocol = 1
const EnvironmentDescriptorPath = "/.well-known/tether/environment"

// EnvironmentTarget is caller-owned remote configuration. Authority is a
// display name, not a grant. Routes are tried in preference order; there is no
// local-catalog or loopback fallback. CredentialReference contains no secret.
type EnvironmentTarget struct {
	EnvironmentID       string             `json:"environmentId"`
	Authority           string             `json:"authority"`
	Routes              []EnvironmentRoute `json:"routes"`
	CredentialReference string             `json:"credentialReference"`
}

type EnvironmentRoute struct {
	BaseURL string `json:"baseURL"`
}

type EnvironmentDescriptor struct {
	EnvironmentID    string                    `json:"environmentId"`
	Label            string                    `json:"label"`
	Platform         EnvironmentPlatform       `json:"platform"`
	ServerVersion    string                    `json:"serverVersion"`
	Protocol         int                       `json:"protocol"`
	Capabilities     map[string]map[string]any `json:"capabilities"`
	UpdateCapability string                    `json:"updateCapability"`
}

type EnvironmentPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// ProtocolMismatchError stops the route walk. UpdateHint is an advisory from
// the server; Error deliberately excludes remote text and secret material.
type ProtocolMismatchError struct {
	RequiredProtocol int
	UpdateHint       string
}

func (e *ProtocolMismatchError) Error() string {
	return fmt.Sprintf("tether: environment protocol mismatch (required %d)", e.RequiredProtocol)
}

type EnvironmentIdentityError struct{}

func (*EnvironmentIdentityError) Error() string { return "tether: environment identity mismatch" }

type EnvironmentAuthenticationError struct{}

func (*EnvironmentAuthenticationError) Error() string {
	return "tether: environment authentication failed"
}

// EnvironmentClock only controls supervision. HTTP deadlines still use
// context deadlines. Tests can advance supervision without sleeping.
type EnvironmentClock interface {
	Now() time.Time
	NewTimer(time.Duration) EnvironmentTimer
}
type EnvironmentTimer interface {
	C() <-chan time.Time
	Stop()
}
type environmentRealClock struct{}

func (environmentRealClock) Now() time.Time { return time.Now() }
func (environmentRealClock) NewTimer(d time.Duration) EnvironmentTimer {
	return environmentRealTimer{time.NewTimer(d)}
}

type environmentRealTimer struct{ *time.Timer }

func (t environmentRealTimer) C() <-chan time.Time { return t.Timer.C }
func (t environmentRealTimer) Stop()               { t.Timer.Stop() }

type EnvironmentOptions struct {
	// ResolveCredential is called only after a descriptor matches identity and
	// protocol. It must resolve the explicit reference, never ambient defaults.
	ResolveCredential func(context.Context, string) (string, error)
	// HTTPClient must use a credential-free transport. The client is cloned;
	// redirects are disabled. Arbitrary RoundTrippers must not inject secrets.
	HTTPClient *http.Client
	SelfURN    string
	Clock      EnvironmentClock
	// Jitter returns a multiplier in [0.5,1.5]. The final delay is capped at 5m.
	Jitter                                        func() float64
	ProbeTimeout, ConnectTimeout                  time.Duration
	StableAfter, PreflightInterval, RouteCooldown time.Duration
}

// EnvironmentClient is immutable and safe to share. Each Subscribe owns its
// cursor, retry schedule and network-wakeup state.
type EnvironmentClient struct {
	target EnvironmentTarget
	opts   EnvironmentOptions
	http   *http.Client
}

func NewEnvironmentClient(target EnvironmentTarget, opts EnvironmentOptions) (*EnvironmentClient, error) {
	if strings.TrimSpace(target.EnvironmentID) == "" || strings.TrimSpace(target.Authority) == "" ||
		strings.TrimSpace(target.CredentialReference) == "" || len(target.Routes) == 0 || len(target.Routes) > 32 || opts.ResolveCredential == nil {
		return nil, errors.New("tether: explicit environment, authority, routes and credential resolver required")
	}
	target.Routes = append([]EnvironmentRoute(nil), target.Routes...)
	for i, route := range target.Routes {
		u, err := url.Parse(route.BaseURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.ForceQuery {
			return nil, errors.New("tether: environment routes require absolute HTTP(S) URLs without credentials, query or fragment")
		}
		target.Routes[i].BaseURL = strings.TrimRight(route.BaseURL, "/")
	}
	hc := http.Client{}
	if opts.HTTPClient != nil {
		hc = *opts.HTTPClient
	}
	// Known credential wrappers cannot be used for anonymous discovery.
	switch hc.Transport.(type) {
	case *credentialTransport, *environmentTransport:
		return nil, errors.New("tether: environment discovery requires a credential-free transport")
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// Public discovery and explicit bearer authentication must not inherit
	// an ambient cookie credential from a caller-owned HTTP client.
	hc.Jar = nil
	hc.Timeout = 0
	if opts.Clock == nil {
		opts.Clock = environmentRealClock{}
	}
	if opts.Jitter == nil {
		opts.Jitter = func() float64 { return 0.5 + rand.Float64() }
	}
	if opts.ProbeTimeout == 0 {
		opts.ProbeTimeout = 2500 * time.Millisecond
	}
	if opts.ConnectTimeout == 0 {
		opts.ConnectTimeout = 15 * time.Second
	}
	if opts.StableAfter == 0 {
		opts.StableAfter = 30 * time.Second
	}
	if opts.PreflightInterval == 0 {
		opts.PreflightInterval = time.Minute
	}
	if opts.RouteCooldown == 0 {
		opts.RouteCooldown = 5 * time.Minute
	}
	if opts.ProbeTimeout < 0 || opts.ConnectTimeout < 0 || opts.StableAfter < 0 || opts.PreflightInterval < 0 || opts.RouteCooldown < 0 {
		return nil, errors.New("tether: environment timeouts must be positive")
	}
	return &EnvironmentClient{target: target, opts: opts, http: &hc}, nil
}

type EnvironmentConnection struct {
	// Client performs one ordinary operation per call; supervision never
	// replays its mutations. Use caller idempotency keys where supported.
	Client     *Client
	Descriptor EnvironmentDescriptor
	RouteIndex int
}

// Connect checks all public descriptors with a short deadline, then tries
// answered routes in preference order and silent routes in a deferred pass.
// Even a silent route must later prove its descriptor before authentication.
func (e *EnvironmentClient) Connect(ctx context.Context) (*EnvironmentConnection, error) {
	return e.connect(ctx, nil)
}

type environmentProbe struct {
	desc EnvironmentDescriptor
	err  error
}

func (e *EnvironmentClient) connect(ctx context.Context, cooldown map[int]time.Time) (*EnvironmentConnection, error) {
	results := make(chan struct {
		index int
		probe environmentProbe
	}, len(e.target.Routes))
	eligible := make([]bool, len(e.target.Routes))
	count := 0
	for i := range e.target.Routes {
		if until, ok := cooldown[i]; ok && e.opts.Clock.Now().Before(until) {
			continue
		}
		eligible[i] = true
		count++
		go func(index int) {
			d, err := e.descriptor(ctx, index, e.opts.ProbeTimeout)
			results <- struct {
				index int
				probe environmentProbe
			}{index, environmentProbe{d, err}}
		}(i)
	}
	probes := make([]environmentProbe, len(eligible))
	for range count {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-results:
			probes[result.index] = result.probe
		}
	}
	var last error = errors.New("tether: environment routes unavailable")
	var blocked error
	transient := false
	for pass := 0; pass < 2; pass++ {
		for i, probe := range probes {
			if !eligible[i] {
				continue
			}
			var mismatch *ProtocolMismatchError
			if errors.As(probe.err, &mismatch) {
				return nil, mismatch
			}
			var identity *EnvironmentIdentityError
			if errors.As(probe.err, &identity) {
				blocked = probe.err
				continue
			}
			if (pass == 0) != (probe.err == nil) {
				continue
			}
			if pass == 1 {
				probe.desc, probe.err = e.descriptor(ctx, i, e.opts.ConnectTimeout)
				if errors.As(probe.err, &mismatch) {
					return nil, mismatch
				}
				if errors.As(probe.err, &identity) {
					blocked = probe.err
					continue
				}
				if probe.err != nil {
					last = probe.err
					transient = true
					continue
				}
			}
			conn, err := e.authenticate(ctx, i, probe.desc)
			if err == nil {
				return conn, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.As(err, &mismatch) {
				return nil, mismatch
			}
			var auth *EnvironmentAuthenticationError
			if errors.As(err, &auth) {
				blocked = err
			} else {
				transient = true
				last = err
			}
			if cooldown != nil {
				cooldown[i] = e.opts.Clock.Now().Add(e.opts.RouteCooldown)
			}
		}
	}
	if !transient && blocked != nil {
		return nil, blocked
	}
	return nil, last
}

func (e *EnvironmentClient) descriptor(ctx context.Context, index int, timeout time.Duration) (EnvironmentDescriptor, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := environmentGET(ctx, e.http, e.target.Routes[index].BaseURL+EnvironmentDescriptorPath)
	if err != nil {
		return EnvironmentDescriptor{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return EnvironmentDescriptor{}, errors.New("tether: environment descriptor unavailable")
	}
	var d EnvironmentDescriptor
	if err := decodeEnvironmentJSON(resp.Body, &d); err != nil {
		return d, err
	}
	if d.EnvironmentID != e.target.EnvironmentID {
		return d, &EnvironmentIdentityError{}
	}
	if d.Protocol != EnvironmentProtocol {
		return d, &ProtocolMismatchError{RequiredProtocol: d.Protocol, UpdateHint: "Update the client or server to a compatible protocol."}
	}
	return d, nil
}

func (e *EnvironmentClient) authenticate(ctx context.Context, index int, descriptor EnvironmentDescriptor) (*EnvironmentConnection, error) {
	ctx, cancel := context.WithTimeout(ctx, e.opts.ConnectTimeout)
	defer cancel()
	token, err := e.opts.ResolveCredential(ctx, e.target.CredentialReference)
	if err != nil || !validBearerToken(token) {
		return nil, &EnvironmentAuthenticationError{}
	}
	baseURL := e.target.Routes[index].BaseURL
	u, _ := url.Parse(baseURL)
	base := e.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	hc := *e.http
	hc.Timeout = e.opts.ConnectTimeout
	hc.Transport = &environmentTransport{base: base, token: token, scheme: u.Scheme, host: u.Host}
	c := &Client{baseURL: baseURL, http: &hc, selfURN: e.opts.SelfURN}
	resp, err := environmentGET(ctx, &hc, baseURL+"/auth/context")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, environmentStatusError(resp, token)
	}
	// The protected read is the preflight; its body is not a secret-store API.
	return &EnvironmentConnection{Client: c, Descriptor: descriptor, RouteIndex: index}, nil
}

type environmentTransport struct {
	base                http.RoundTripper
	token, scheme, host string
}

func (t *environmentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != t.scheme || req.URL.Host != t.host {
		return nil, errors.New("tether: environment request changed origin")
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	r.Header.Set("Tether-Protocol", fmt.Sprint(EnvironmentProtocol))
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, errors.New("tether: environment transport unavailable")
	}
	return resp, nil
}

func (t *environmentTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func environmentGET(ctx context.Context, hc *http.Client, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("tether: invalid environment request")
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("tether: environment transport unavailable")
	}
	return resp, nil
}

func decodeEnvironmentJSON(r io.Reader, out any) error {
	b, err := io.ReadAll(io.LimitReader(r, (16<<20)+1))
	if err != nil || len(b) > 16<<20 || json.Unmarshal(b, out) != nil {
		return errors.New("tether: invalid environment response")
	}
	return nil
}

func environmentStatusError(resp *http.Response, token string) error {
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &EnvironmentAuthenticationError{}
	}
	if resp.StatusCode == http.StatusConflict {
		var wire struct {
			Error struct {
				Code             string `json:"code"`
				RequiredProtocol int    `json:"required_protocol"`
				UpdateHint       string `json:"update_hint"`
			} `json:"error"`
		}
		if decodeEnvironmentJSON(resp.Body, &wire) == nil && wire.Error.Code == "protocol_mismatch" {
			hint := wire.Error.UpdateHint
			if token != "" {
				hint = strings.ReplaceAll(hint, token, "[redacted]")
			}
			if len(hint) > 1024 {
				hint = "Update the client or server to a compatible protocol."
			}
			return &ProtocolMismatchError{RequiredProtocol: wire.Error.RequiredProtocol, UpdateHint: hint}
		}
	}
	return fmt.Errorf("tether: environment HTTP status %d", resp.StatusCode)
}
