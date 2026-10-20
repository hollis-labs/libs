package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Issued is a freshly minted token and the instant it stops being valid.
type Issued struct {
	Token     string
	ExpiresAt time.Time
}

// MintFunc asks the issuer for a new token.
type MintFunc func(ctx context.Context) (Issued, error)

// ErrNoToken is returned by Refresher.Token when no valid token is held and a
// new one could not be minted. It wraps the mint error.
var ErrNoToken = errors.New("auth: no valid token")

// DefaultRefreshFraction is the share of a token's life after which a
// Refresher mints a replacement: a 15 minute token is renewed at 10 minutes,
// leaving a third of its life to ride out a slow or briefly unavailable issuer.
const DefaultRefreshFraction = 2.0 / 3.0

// RefresherConfig configures a Refresher.
type RefresherConfig struct {
	Mint MintFunc // required
	// Fraction of the token's life after which it is renewed; default
	// DefaultRefreshFraction. Must be in (0, 1).
	Fraction float64
	// Skew is how long before expiry a token is no longer handed out, so it
	// does not expire in flight; default 5s.
	Skew time.Duration
	// RetryMin and RetryMax bound the delay between failed mint attempts
	// (doubling); defaults 2s and 60s. A failed renewal keeps serving the old
	// token until it is within Skew of expiry.
	RetryMin, RetryMax time.Duration
	// OnRefresh, optional, runs after every mint attempt: with the new expiry
	// and a nil error, or the zero Issued and the error. It never sees the
	// token itself.
	OnRefresh func(expiresAt time.Time, err error)
	Now       func() time.Time
}

// Refresher keeps one short-lived token (for example a 15 minute agent JWT)
// valid for as long as its process runs. It is safe for concurrent use.
//
// Token renews lazily on demand, so a caller needs no background goroutine;
// Run additionally renews ahead of demand so the first caller after a renewal
// point never waits on the issuer. The token is never logged or returned in an
// error.
type Refresher struct {
	cfg RefresherConfig

	sem chan struct{} // capacity 1: serializes mints, held across the issuer call; waiters honor ctx

	mu      sync.Mutex // guards the fields below
	tok     string
	exp     time.Time
	due     time.Time // renew at or after this instant
	nextTry time.Time // do not retry a failed mint before this instant
	delay   time.Duration
	lastErr error
}

// NewRefresher validates cfg and returns a Refresher holding no token yet.
func NewRefresher(cfg RefresherConfig) (*Refresher, error) {
	if cfg.Mint == nil {
		return nil, errors.New("auth: RefresherConfig.Mint is required")
	}
	if cfg.Fraction == 0 {
		cfg.Fraction = DefaultRefreshFraction
	}
	if cfg.Fraction <= 0 || cfg.Fraction >= 1 {
		return nil, errors.New("auth: RefresherConfig.Fraction must be between 0 and 1")
	}
	if cfg.Skew <= 0 {
		cfg.Skew = 5 * time.Second
	}
	if cfg.RetryMin <= 0 {
		cfg.RetryMin = 2 * time.Second
	}
	if cfg.RetryMax < cfg.RetryMin {
		cfg.RetryMax = 60 * time.Second
		if cfg.RetryMax < cfg.RetryMin {
			cfg.RetryMax = cfg.RetryMin
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Refresher{cfg: cfg, sem: make(chan struct{}, 1)}, nil
}

// Token returns a token that is valid now, minting or renewing it if needed.
// Past the renewal point it tries to renew but, if the issuer fails, keeps
// returning the held token until it is within Skew of expiry. After that it
// returns ErrNoToken (wrapping the last mint error).
func (r *Refresher) Token(ctx context.Context) (string, error) {
	if tok, ok, due := r.held(); ok && !due {
		return tok, nil
	} else if ok {
		// Valid but due: renew if nobody else is, otherwise serve the old one.
		select {
		case r.sem <- struct{}{}:
			r.refreshLocked(ctx)
			<-r.sem
		default:
		}
		if tok, ok, _ := r.held(); ok {
			return tok, nil
		}
		return "", r.noToken()
	}
	// Nothing usable is held: wait for the in-flight mint, or mint, but give
	// up as soon as this caller's ctx is done.
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-r.sem }()
	if tok, ok, _ := r.held(); ok { // another caller minted while we waited
		return tok, nil
	}
	r.refreshLocked(ctx)
	if tok, ok, _ := r.held(); ok {
		return tok, nil
	}
	return "", r.noToken()
}

// Run renews the token ahead of demand until ctx is done: it mints now, then
// again at each renewal point, retrying with backoff after a failure. Errors
// are reported through OnRefresh.
func (r *Refresher) Run(ctx context.Context) {
	for {
		select {
		case r.sem <- struct{}{}:
			if _, ok, due := r.held(); !ok || due {
				r.refreshLocked(ctx)
			}
			<-r.sem
		case <-ctx.Done():
			return
		}
		wait := r.untilNext()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// ExpiresAt is the held token's expiry, or the zero time if none is held.
func (r *Refresher) ExpiresAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exp
}

// held reports the current token, whether it is still usable (not within Skew
// of expiry) and whether it is past its renewal point.
func (r *Refresher) held() (tok string, ok, due bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.cfg.Now()
	if r.tok == "" || !now.Add(r.cfg.Skew).Before(r.exp) {
		return "", false, true
	}
	return r.tok, true, !now.Before(r.due)
}

func (r *Refresher) noToken() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastErr != nil {
		return fmt.Errorf("%w: %w", ErrNoToken, r.lastErr)
	}
	return ErrNoToken
}

// untilNext is how long Run should sleep before it next needs to act.
func (r *Refresher) untilNext() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.cfg.Now()
	at := r.due
	if r.tok == "" || !now.Add(r.cfg.Skew).Before(r.exp) || r.lastErr != nil {
		at = r.nextTry
	}
	if d := at.Sub(now); d > 0 {
		return d
	}
	return r.cfg.RetryMin
}

// refreshLocked mints while the caller holds sem, honoring the retry backoff.
func (r *Refresher) refreshLocked(ctx context.Context) {
	r.mu.Lock()
	if r.cfg.Now().Before(r.nextTry) {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	start := r.cfg.Now()
	got, err := r.cfg.Mint(ctx)
	// Usable means still valid, with Skew to spare, when it arrives: judged
	// against the clock now, not the instant the request started. An unusable
	// issuance is a failure, so the token already held is kept and the retry
	// backoff applies.
	if err == nil && (got.Token == "" || !r.cfg.Now().Add(r.cfg.Skew).Before(got.ExpiresAt)) {
		err = errors.New("auth: issuer returned an unusable token")
	}
	r.mu.Lock()
	if err != nil {
		if r.delay == 0 {
			r.delay = r.cfg.RetryMin
		} else if r.delay *= 2; r.delay > r.cfg.RetryMax {
			r.delay = r.cfg.RetryMax
		}
		r.nextTry = r.cfg.Now().Add(r.delay)
		r.lastErr = err
	} else {
		r.tok, r.exp = got.Token, got.ExpiresAt
		r.due = start.Add(time.Duration(float64(got.ExpiresAt.Sub(start)) * r.cfg.Fraction))
		r.delay, r.nextTry, r.lastErr = 0, time.Time{}, nil
	}
	cb := r.cfg.OnRefresh
	exp := r.exp
	r.mu.Unlock()
	if cb != nil {
		if err != nil {
			cb(time.Time{}, err)
		} else {
			cb(exp, nil)
		}
	}
}

// HTTPMinter is a MintFunc-compatible client for a token endpoint that takes a
// JSON request authenticated by a parent bearer credential and answers JSON
// with a "token" and either "expires_at" (RFC 3339) or "ttl_seconds". This is
// the shape of Tether's POST /auth/token. The caller supplies the request body
// and any required headers, so nothing here is specific to one issuer.
type HTTPMinter struct {
	URL string
	// Body is marshaled as the JSON request body on every mint.
	Body any
	// Credential returns the parent bearer credential. It is called on every
	// mint so a renewed credential on disk is picked up (for example a closure
	// over tokenfile.Read).
	Credential func() (string, error)
	// Header holds extra request headers (for example a protocol version).
	Header http.Header
	Client *http.Client // default 15s timeout; cross-origin redirects are refused
	Now    func() time.Time
}

// Mint requests a token. Errors carry the HTTP status at most: never the issuer's
// reply body, the URL, the credential or the token.
func (m *HTTPMinter) Mint(ctx context.Context) (Issued, error) {
	if m.URL == "" || m.Credential == nil {
		return Issued{}, errors.New("auth: HTTPMinter needs URL and Credential")
	}
	cred, err := m.Credential()
	if err != nil {
		return Issued{}, fmt.Errorf("auth: parent credential: %w", err)
	}
	body, err := json.Marshal(m.Body)
	if err != nil {
		return Issued{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.URL, bytes.NewReader(body))
	if err != nil {
		// The URL is operator configuration and may embed a secret; never echo it.
		return Issued{}, errors.New("auth: invalid token endpoint URL")
	}
	for k, vs := range m.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Authorization", "Bearer "+cred)
	req.Header.Set("Content-Type", "application/json")
	c := m.Client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	origin := req.URL.Scheme + "://" + req.URL.Host
	cc := *c
	cc.CheckRedirect = func(r *http.Request, _ []*http.Request) error {
		if r.URL.Scheme+"://"+r.URL.Host != origin {
			return errors.New("auth: refusing cross-origin redirect")
		}
		return nil
	}
	resp, err := cc.Do(req) // #nosec G704 -- URL is operator configuration
	if err != nil {
		return Issued{}, fmt.Errorf("auth: token request failed: %w", scrubURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode != http.StatusOK {
		// Status only: an endpoint can echo the Authorization header or any
		// other secret into its body, so the body is never surfaced.
		return Issued{}, fmt.Errorf("auth: issuer refused token: status %d", resp.StatusCode)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
		TTL       int       `json:"ttl_seconds"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Token == "" {
		return Issued{}, errors.New("auth: issuer returned an unreadable token")
	}
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	exp := out.ExpiresAt
	if exp.IsZero() && out.TTL > 0 {
		exp = now().Add(time.Duration(out.TTL) * time.Second)
	}
	if exp.IsZero() {
		if e, ok := jwtExpiry(out.Token); ok {
			exp = e
		}
	}
	if exp.IsZero() {
		return Issued{}, errors.New("auth: issuer reply carries no expiry")
	}
	return Issued{Token: out.Token, ExpiresAt: exp}, nil
}

// scrubURLError drops the request URL from a transport error so a URL that
// embeds a secret in its query cannot leak into logs.
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// jwtExpiry reads the exp claim of a JWT without verifying it, for scheduling
// renewal only. It is never used to trust a token.
func jwtExpiry(tok string) (time.Time, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(b, &c) != nil || c.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(c.Exp, 0), true
}
