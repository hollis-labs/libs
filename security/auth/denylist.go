package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// DenyList holds revoked token ids (jti), principals (sub) and agent sessions
// (sid). It is safe for concurrent use.
type DenyList struct {
	mu  sync.RWMutex
	jti map[string]struct{}
	sub map[string]struct{}
	sid map[string]struct{}
}

// DenyListDoc is the wire format served by the issuer's deny-list endpoint.
type DenyListDoc struct {
	JTI []string `json:"jti"`
	Sub []string `json:"sub"`
	SID []string `json:"sid"`
}

// NewDenyList returns an empty list.
func NewDenyList() *DenyList { return &DenyList{} }

// Replace swaps the whole list.
func (d *DenyList) Replace(doc DenyListDoc) {
	set := func(v []string) map[string]struct{} {
		m := make(map[string]struct{}, len(v))
		for _, s := range v {
			m[s] = struct{}{}
		}
		return m
	}
	d.mu.Lock()
	d.jti, d.sub, d.sid = set(doc.JTI), set(doc.Sub), set(doc.SID)
	d.mu.Unlock()
}

// Denied reports whether any of the principal's identifiers is revoked.
func (d *DenyList) Denied(p Principal) bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, a := d.jti[p.TokenID]
	_, b := d.sub[p.ID]
	_, c := d.sid[p.SessionID]
	return (a && p.TokenID != "") || (b && p.ID != "") || (c && p.SessionID != "")
}

// DenyListPoller polls an issuer endpoint with ETag revalidation and keeps a
// DenyList current. A failed poll keeps the last known list (tokens are short
// lived, so revocation latency is bounded by TTL even if the issuer is down).
type DenyListPoller struct {
	URL      string
	List     *DenyList
	Client   *http.Client
	Interval time.Duration // default 30s
	Header   http.Header   // optional, e.g. Authorization for the endpoint
	Logger   *slog.Logger

	etag string
}

// Poll performs one conditional fetch.
func (p *DenyListPoller) Poll(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return err
	}
	for k, v := range p.Header {
		req.Header[k] = v
	}
	if p.etag != "" {
		req.Header.Set("If-None-Match", p.etag)
	}
	c := p.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("auth: deny-list poll: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil
	case http.StatusOK:
	default:
		return fmt.Errorf("auth: deny-list poll: status %d", resp.StatusCode)
	}
	var doc DenyListDoc
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&doc); err != nil {
		return fmt.Errorf("auth: deny-list decode: %w", err)
	}
	p.List.Replace(doc)
	p.etag = resp.Header.Get("ETag")
	return nil
}

// Run polls until ctx is done. Errors are logged, not returned.
func (p *DenyListPoller) Run(ctx context.Context) {
	iv := p.Interval
	if iv <= 0 {
		iv = 30 * time.Second
	}
	lg := p.Logger
	if lg == nil {
		lg = slog.Default()
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		if err := p.Poll(ctx); err != nil && ctx.Err() == nil {
			lg.Warn("auth: deny-list poll failed; keeping last list", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
