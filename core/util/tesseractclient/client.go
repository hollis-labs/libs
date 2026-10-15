package tesseract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultBaseURL is where Tesseract's API service listens on this machine.
// It answers on loopback without a credential.
// maxNamespacePages bounds ListNamespaces: 1000 pages of 200 is 200,000
// namespaces, far beyond any real registry.
const maxNamespacePages = 1000

const DefaultBaseURL = "http://127.0.0.1:8089"

const (
	defaultTimeout          = 20 * time.Second
	defaultMaxResponseBytes = 64 << 20
	defaultPageLimit        = 500
	maxErrorText            = 300
)

// Client talks to one Tesseract. It is safe for concurrent use.
type Client struct {
	baseURL  string
	token    string
	http     *http.Client
	maxBytes int64
}

type clientOptions struct {
	timeout  time.Duration
	maxBytes int64
}

// Option configures New.
type Option func(*clientOptions)

// WithTimeout overrides the per-request timeout (default 20s, Station's
// value; Tangent independently chose 10s). A value <= 0 is ignored.
func WithTimeout(d time.Duration) Option {
	return func(o *clientOptions) {
		if d > 0 {
			o.timeout = d
		}
	}
}

// WithMaxResponseBytes overrides the response-size ceiling (default 64 MiB,
// Station's value; Tangent independently chose 8 MiB). A response larger than
// the cap is an error matching ErrResponseTooLarge and ErrUnavailable, never a
// truncated decode. A value <= 0 is ignored.
func WithMaxResponseBytes(n int64) Option {
	return func(o *clientOptions) {
		if n > 0 {
			o.maxBytes = n
		}
	}
}

// New builds a client. An empty baseURL means DefaultBaseURL; an empty token
// means no Authorization header, which is the local service's normal state. A
// non-empty token is sent as a static "Bearer" credential on every request,
// including the public readiness probe; there is no session or refresh.
func New(baseURL, token string, opts ...Option) *Client {
	o := clientOptions{timeout: defaultTimeout, maxBytes: defaultMaxResponseBytes}
	for _, opt := range opts {
		opt(&o)
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		token:    token,
		http:     &http.Client{Timeout: o.timeout},
		maxBytes: o.maxBytes,
	}
}

// BaseURL is the Tesseract this client points at.
func (c *Client) BaseURL() string { return c.baseURL }

// Recall issues one recall (POST /v1/memory/recall) and returns the page with
// its manifest.
func (c *Client) Recall(ctx context.Context, req RecallRequest) (RecallPage, error) {
	var page RecallPage
	if err := c.do(ctx, http.MethodPost, "/v1/memory/recall", nil, req, &page); err != nil {
		return RecallPage{}, err
	}
	return page, nil
}

// RecallAll pages a recall to exhaustion by following manifest.next_cursor,
// and reports whether it got everything. It stops at maxRecords rather than
// paging without bound; in that case complete is false and the caller must
// say so instead of presenting the result as the whole set. maxRecords <= 0
// means "the first page only": the loop always fetches one page, and returns
// it with complete false if the server has more. A zero req.Limit becomes a
// page size of 500.
//
// A server (or proxy) that hands back a cursor this call has already used
// (the same one twice running, or a longer cycle such as A, B, A) is reported
// as an error rather than looped on forever, whether or not the pages carry
// results.
func (c *Client) RecallAll(ctx context.Context, req RecallRequest, maxRecords int) (revisions []Revision, complete bool, err error) {
	if req.Limit == 0 {
		req.Limit = defaultPageLimit
	}
	seen := map[string]struct{}{}
	if req.Cursor != "" {
		seen[req.Cursor] = struct{}{}
	}
	for {
		page, err := c.Recall(ctx, req)
		if err != nil {
			return nil, false, err
		}
		for _, r := range page.Results {
			revisions = append(revisions, r.Revision)
		}
		next := page.Manifest.NextCursor
		if next == "" {
			return revisions, true, nil
		}
		if len(revisions) >= maxRecords {
			return revisions, false, nil
		}
		if _, dup := seen[next]; dup {
			return nil, false, fmt.Errorf("tesseract: recall repeated cursor %q without advancing", next)
		}
		seen[next] = struct{}{}
		req.Cursor = next
	}
}

// GetCurrent returns the current revision of one knowledge record
// (GET /v1/knowledge/current). A missing key is an error matching
// ErrNotFound. It reads the knowledge domain only; memory-domain records live
// behind a different route this client does not cover.
func (c *Client) GetCurrent(ctx context.Context, namespace, key string) (Revision, error) {
	q := url.Values{"namespace": {namespace}, "key": {key}}
	var rev Revision
	if err := c.do(ctx, http.MethodGet, "/v1/knowledge/current", q, nil, &rev); err != nil {
		return Revision{}, err
	}
	return rev, nil
}

// GetRevision hydrates one revision by id (GET /v1/memory/revisions/{id}),
// including a deprecated one, which recall does not return. Tangent-only
// today. An unknown id is an error matching ErrNotFound.
func (c *Client) GetRevision(ctx context.Context, revisionID string) (Revision, error) {
	if revisionID == "" {
		return Revision{}, errors.New("tesseract: get revision needs a revision_id")
	}
	var rev Revision
	if err := c.do(ctx, http.MethodGet, "/v1/memory/revisions/"+url.PathEscape(revisionID), nil, nil, &rev); err != nil {
		return Revision{}, err
	}
	return rev, nil
}

// Deprecate retires one revision (POST /v1/memory/deprecate). It is
// idempotent on the server: deprecating an already-deprecated revision is not
// an error. An unknown id is an error matching ErrNotFound. Tangent-only
// today, and the only write either app makes.
func (c *Client) Deprecate(ctx context.Context, revisionID string) error {
	if revisionID == "" {
		return errors.New("tesseract: deprecate needs a revision_id")
	}
	return c.do(ctx, http.MethodPost, "/v1/memory/deprecate", nil, map[string]string{"revision_id": revisionID}, nil)
}

// ListNamespaces returns the registered namespaces under prefix
// (GET /v1/namespaces/list), following the server's cursor to the end.
// Defined by Station but not called in its production code today.
//
// It gives up with an error on a repeated cursor (including a cycle such as
// A, B, A) and after maxNamespacePages pages, so a broken server or proxy
// cannot make it grow its result without bound.
func (c *Client) ListNamespaces(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	cursor := ""
	seen := map[string]struct{}{}
	for pages := 0; ; pages++ {
		if pages >= maxNamespacePages {
			return nil, fmt.Errorf("tesseract: namespace listing exceeded %d pages", maxNamespacePages)
		}
		q := url.Values{"prefix": {prefix}, "limit": {"200"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var body struct {
			Items []struct {
				Namespace string `json:"namespace"`
			} `json:"items"`
			Truncated  bool   `json:"truncated"`
			NextCursor string `json:"next_cursor"`
		}
		if err := c.do(ctx, http.MethodGet, "/v1/namespaces/list", q, nil, &body); err != nil {
			return nil, err
		}
		for _, it := range body.Items {
			out = append(out, it.Namespace)
		}
		if !body.Truncated || body.NextCursor == "" {
			return out, nil
		}
		if _, dup := seen[body.NextCursor]; dup || body.NextCursor == cursor {
			return nil, fmt.Errorf("tesseract: namespace listing repeated cursor %q without advancing", body.NextCursor)
		}
		seen[body.NextCursor] = struct{}{}
		cursor = body.NextCursor
	}
}

// Health reports whether Tesseract answers at all (GET /v1/health/readiness).
// The readiness route is public, so this separates "the service is down" from
// "this caller is not authorized". Defined by both apps but exercised only by
// their own tests today.
func (c *Client) Health(ctx context.Context) error {
	var ignored json.RawMessage
	return c.do(ctx, http.MethodGet, "/v1/health/readiness", nil, nil, &ignored)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("tesseract: encode request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return fmt.Errorf("tesseract: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnavailable, c.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read one byte past the cap so an over-cap body is detected, not
	// silently cut short and handed to the decoder.
	limit := c.maxBytes
	if limit < math.MaxInt64 {
		limit++
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return fmt.Errorf("%w: reading %s: %w", ErrUnavailable, path, err)
	}
	if int64(len(raw)) > c.maxBytes {
		return fmt.Errorf("%w: %w: %s answered more than %d bytes", ErrUnavailable, ErrResponseTooLarge, path, c.maxBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newAPIError(resp.StatusCode, path, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: decoding %s: %w", ErrUnavailable, path, err)
	}
	return nil
}

// newAPIError extracts Tesseract's {code, message, details} error, falling
// back to the raw text and then the status line, so its validation messages
// (which name the offending field) are never replaced by a bare status.
func newAPIError(status int, path string, raw []byte) *APIError {
	apiErr := &APIError{Status: status, Path: path}
	var parsed struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	switch text := strings.TrimSpace(string(raw)); {
	case json.Unmarshal(raw, &parsed) == nil && parsed.Message != "":
		apiErr.Code, apiErr.Message = parsed.Code, parsed.Message
	case text != "":
		apiErr.Message = truncate(text, maxErrorText)
	default:
		apiErr.Message = http.StatusText(status)
	}
	return apiErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
