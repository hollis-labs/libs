package tether

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
	selfURN string
}

type Option func(*Client)

func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.http = httpClient
		}
	}
}

func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		if baseURL != "" {
			c.baseURL = strings.TrimRight(baseURL, "/")
		}
	}
}

// WithSelfURN configures the caller's own asserted identity (a `msg://...`
// URN). Tether's messaging routes require every read to name a caller via
// `?as=` (ADR 0045's same-host, self-asserted trust model) -- Get and
// Thread have no recipient/address parameter of their own to derive that
// claim from (unlike Inbox/Subscribe, which already take an explicit
// recipient and use it directly), so a Client used for those two calls
// must configure its own identity once, here, up front.
func WithSelfURN(urn string) Option {
	return func(c *Client) {
		c.selfURN = urn
	}
}

func New(listenAddr string, opts ...Option) (*Client, error) {
	if listenAddr == "" {
		listenAddr = DefaultListenAddr
	}
	listenAddr = expandListenAddr(listenAddr)

	baseURL, httpClient, err := transportFor(listenAddr)
	if err != nil {
		return nil, err
	}
	c := &Client{
		baseURL: baseURL,
		http:    httpClient,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

func MustNew(listenAddr string, opts ...Option) *Client {
	c, err := New(listenAddr, opts...)
	if err != nil {
		panic(err)
	}
	return c
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	if err := c.getJSON(ctx, "/health", &h); err != nil {
		return Health{}, err
	}
	return h, nil
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Health(ctx)
	return err
}

func (c *Client) CreateSession(ctx context.Context, launchID string) (LaunchResponse, error) {
	var out LaunchResponse
	err := c.doJSON(ctx, http.MethodPost, "/sessions", LaunchRequest{Launch: launchID}, http.StatusCreated, &out)
	return out, err
}

func (c *Client) CreateSessionWithBootPrompt(ctx context.Context, launchID, bootPrompt string) (LaunchResponse, error) {
	var out LaunchResponse
	err := c.doJSON(ctx, http.MethodPost, "/sessions", LaunchRequest{Launch: launchID, BootPrompt: bootPrompt}, http.StatusCreated, &out)
	return out, err
}

func (c *Client) CreateSessionWithInput(ctx context.Context, req LaunchRequest) (LaunchResponse, error) {
	var out LaunchResponse
	err := c.doJSON(ctx, http.MethodPost, "/sessions", req, http.StatusCreated, &out)
	return out, err
}

func (c *Client) LaunchSession(ctx context.Context, sessionID string) (LaunchResponse, error) {
	var out LaunchResponse
	err := c.doJSONWithClient(ctx, c.longLivedClient(), http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/launch", nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Launch(ctx context.Context, launchID string) (LaunchResponse, error) {
	created, err := c.CreateSession(ctx, launchID)
	if err != nil {
		return LaunchResponse{}, err
	}
	return c.LaunchSession(ctx, created.ID)
}

func (c *Client) LaunchWithInput(ctx context.Context, req LaunchRequest) (LaunchResponse, error) {
	created, err := c.CreateSessionWithInput(ctx, req)
	if err != nil {
		return LaunchResponse{}, err
	}
	return c.LaunchSession(ctx, created.ID)
}

func (c *Client) ListSessions(ctx context.Context, opts ListSessionsOptions) (ListSessionsResponse, error) {
	q := url.Values{}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	if opts.State != "" {
		q.Set("state", opts.State)
	}
	var out ListSessionsResponse
	if err := c.getJSON(ctx, withQuery("/sessions", q), &out); err != nil {
		return ListSessionsResponse{}, err
	}
	return out, nil
}

func (c *Client) GetSession(ctx context.Context, sessionID string) (Session, error) {
	var out Session
	if err := c.getJSON(ctx, "/sessions/"+url.PathEscape(sessionID), &out); err != nil {
		return Session{}, err
	}
	return out, nil
}

func (c *Client) StopSession(ctx context.Context, sessionID string) error {
	return c.doNoBody(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/stop", nil, http.StatusNoContent)
}

func (c *Client) ResizeSession(ctx context.Context, sessionID string, rows, cols uint16) error {
	return c.doNoBody(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/resize", ResizeRequest{Rows: rows, Cols: cols}, http.StatusNoContent)
}

func (c *Client) SendInput(ctx context.Context, sessionID string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/sessions/"+url.PathEscape(sessionID)+"/input", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return wrapIfUnreachable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return readAPIError(resp)
	}
	return nil
}

func (c *Client) SendTurn(ctx context.Context, sessionID, text string) error {
	body := map[string]string{"text": text}
	return c.doNoBodyWithClient(ctx, c.longLivedClient(), http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/turn", body, http.StatusNoContent)
}

func (c *Client) AttachSession(ctx context.Context, sessionID string, w io.Writer, sinceSeq int64) error {
	q := url.Values{}
	if sinceSeq > 0 {
		q.Set("since_seq", strconv.FormatInt(sinceSeq, 10))
	}
	resp, err := c.doStream(ctx, "/sessions/"+url.PathEscape(sessionID)+"/attach", q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return nil
}

func (c *Client) WaitSession(ctx context.Context, sessionID string) (int, error) {
	resp, err := c.doStream(ctx, "/sessions/"+url.PathEscape(sessionID)+"/wait", nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out WaitResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decode wait response: %w", err)
	}
	return out.ExitCode, nil
}

func (c *Client) CreateCheckpoint(ctx context.Context, sessionID string, req CheckpointCreateRequest) (Checkpoint, error) {
	var out Checkpoint
	err := c.doJSON(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/checkpoint", req, http.StatusCreated, &out)
	return out, err
}

func (c *Client) ListCheckpoints(ctx context.Context, logicalAgentID string) (CheckpointListResponse, error) {
	var out CheckpointListResponse
	if err := c.getJSON(ctx, "/logical-agents/"+url.PathEscape(logicalAgentID)+"/checkpoints", &out); err != nil {
		return CheckpointListResponse{}, err
	}
	return out, nil
}

func (c *Client) CreateEnvelope(ctx context.Context, req EnvelopeCreateRequest) (Envelope, error) {
	var out Envelope
	err := c.doJSON(ctx, http.MethodPost, "/broker/envelopes", req, http.StatusCreated, &out)
	return out, err
}

func (c *Client) ListEnvelopes(ctx context.Context, opts EnvelopeListOptions) (EnvelopeListResponse, error) {
	q := url.Values{}
	if opts.Recipient != "" {
		q.Set("recipient", opts.Recipient)
	}
	if opts.WorkflowID != "" {
		q.Set("workflow_id", opts.WorkflowID)
	}
	if opts.CorrelationID != "" {
		q.Set("correlation_id", opts.CorrelationID)
	}
	var out EnvelopeListResponse
	if err := c.getJSON(ctx, withQuery("/broker/envelopes", q), &out); err != nil {
		return EnvelopeListResponse{}, err
	}
	return out, nil
}

func (c *Client) GetEnvelope(ctx context.Context, envelopeID string) (Envelope, error) {
	var out Envelope
	if err := c.getJSON(ctx, "/broker/envelopes/"+url.PathEscape(envelopeID), &out); err != nil {
		return Envelope{}, err
	}
	return out, nil
}

func (c *Client) ReplyEnvelope(ctx context.Context, envelopeID string, req EnvelopeCreateRequest) (Envelope, error) {
	var out Envelope
	err := c.doJSON(ctx, http.MethodPost, "/broker/envelopes/"+url.PathEscape(envelopeID)+"/reply", req, http.StatusCreated, &out)
	return out, err
}

func (c *Client) ListSessionEvents(ctx context.Context, sessionID string, opts EventListOptions) (EventListResponse, error) {
	q := url.Values{}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor > 0 {
		q.Set("cursor", strconv.FormatInt(opts.Cursor, 10))
	}
	if opts.SinceSeq > 0 {
		q.Set("since_seq", strconv.FormatInt(opts.SinceSeq, 10))
	}
	for _, scope := range opts.Scopes {
		if strings.TrimSpace(scope) != "" {
			q.Add("scope", scope)
		}
	}
	for _, kind := range opts.Kinds {
		if strings.TrimSpace(kind) != "" {
			q.Add("kind", kind)
		}
	}
	var out EventListResponse
	if err := c.getJSON(ctx, withQuery("/sessions/"+url.PathEscape(sessionID)+"/events", q), &out); err != nil {
		return EventListResponse{}, err
	}
	return out, nil
}

func (c *Client) ListEvents(ctx context.Context, opts EventListOptions) (EventListResponse, error) {
	q := url.Values{}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor > 0 {
		q.Set("cursor", strconv.FormatInt(opts.Cursor, 10))
	}
	if opts.SinceSeq > 0 {
		q.Set("since_seq", strconv.FormatInt(opts.SinceSeq, 10))
	}
	for _, scope := range opts.Scopes {
		if strings.TrimSpace(scope) != "" {
			q.Add("scope", scope)
		}
	}
	for _, kind := range opts.Kinds {
		if strings.TrimSpace(kind) != "" {
			q.Add("kind", kind)
		}
	}
	var out EventListResponse
	if err := c.getJSON(ctx, withQuery("/events", q), &out); err != nil {
		return EventListResponse{}, err
	}
	return out, nil
}

func (c *Client) StreamEvents(ctx context.Context, opts StreamEventsOptions) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		errs <- c.streamEvents(ctx, opts, events)
	}()
	return events, errs
}

func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var out struct {
		Projects []Project `json:"projects"`
	}
	if err := c.getJSON(ctx, "/catalog/projects", &out); err != nil {
		return nil, err
	}
	return out.Projects, nil
}

func (c *Client) ListAgents(ctx context.Context) ([]Agent, error) {
	var out struct {
		Agents []Agent `json:"agents"`
	}
	if err := c.getJSON(ctx, "/catalog/agents", &out); err != nil {
		return nil, err
	}
	return out.Agents, nil
}

func (c *Client) ListProviders(ctx context.Context) ([]Provider, error) {
	var out struct {
		Providers []Provider `json:"providers"`
	}
	if err := c.getJSON(ctx, "/catalog/providers", &out); err != nil {
		return nil, err
	}
	return out.Providers, nil
}

func (c *Client) ListLaunches(ctx context.Context) ([]Launch, error) {
	var out struct {
		Launches []Launch `json:"launches"`
	}
	if err := c.getJSON(ctx, "/catalog/launches", &out); err != nil {
		return nil, err
	}
	return out.Launches, nil
}

func (c *Client) ListAIProviders(ctx context.Context) (ListAIProvidersResponse, error) {
	var out ListAIProvidersResponse
	if err := c.getJSON(ctx, "/ai/providers", &out); err != nil {
		return ListAIProvidersResponse{}, err
	}
	return out, nil
}

func (c *Client) ListAIModels(ctx context.Context, providerID string) (ListAIModelsResponse, error) {
	path := "/ai/models"
	if providerID != "" {
		path = withQuery(path, url.Values{"provider_id": []string{providerID}})
	}
	var out ListAIModelsResponse
	if err := c.getJSON(ctx, path, &out); err != nil {
		return ListAIModelsResponse{}, err
	}
	return out, nil
}

func (c *Client) ListAIRoutes(ctx context.Context) (ListAIRoutesResponse, error) {
	var out ListAIRoutesResponse
	if err := c.getJSON(ctx, "/ai/routes", &out); err != nil {
		return ListAIRoutesResponse{}, err
	}
	return out, nil
}

func (c *Client) PreviewAIRoute(ctx context.Context, req ChatRequest) (RoutePreviewResponse, error) {
	var out RoutePreviewResponse
	err := c.doJSON(ctx, http.MethodPost, "/ai/routes/preview", req, http.StatusOK, &out)
	return out, err
}

func (c *Client) ExplainAIRoute(ctx context.Context, req ChatRequest) (RouteExplainResponse, error) {
	var out RouteExplainResponse
	err := c.doJSON(ctx, http.MethodPost, "/ai/routes/explain", req, http.StatusOK, &out)
	return out, err
}

func (c *Client) AIChat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var out ChatResponse
	err := c.doJSONWithClient(ctx, c.longLivedClient(), http.MethodPost, "/ai/chat", req, http.StatusOK, &out)
	return out, err
}

func (c *Client) AIAudit(ctx context.Context, q AIAuditQuery) (AIAuditResponse, error) {
	params := url.Values{}
	if q.EventType != "" {
		params.Set("event_type", q.EventType)
	}
	if q.Provider != "" {
		params.Set("provider", q.Provider)
	}
	if q.Model != "" {
		params.Set("model", q.Model)
	}
	if q.SessionID != "" {
		params.Set("session_id", q.SessionID)
	}
	if q.CallerID != "" {
		params.Set("caller_id", q.CallerID)
	}
	if q.Limit > 0 {
		params.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Since != "" {
		params.Set("since", q.Since)
	}
	if q.ErrorsOnly {
		params.Set("errors_only", "true")
	}
	var out AIAuditResponse
	if err := c.getJSON(ctx, withQuery("/ai/audit", params), &out); err != nil {
		return AIAuditResponse{}, err
	}
	return out, nil
}

func (c *Client) AIUsage(ctx context.Context, q AIUsageQuery) (AIUsageResponse, error) {
	params := url.Values{}
	if q.Provider != "" {
		params.Set("provider", q.Provider)
	}
	if q.Model != "" {
		params.Set("model", q.Model)
	}
	if q.SessionID != "" {
		params.Set("session_id", q.SessionID)
	}
	if q.CallerID != "" {
		params.Set("caller_id", q.CallerID)
	}
	if q.Operation != "" {
		params.Set("operation", q.Operation)
	}
	if q.Since != "" {
		params.Set("since", q.Since)
	}
	var out AIUsageResponse
	if err := c.getJSON(ctx, withQuery("/ai/usage", params), &out); err != nil {
		return AIUsageResponse{}, err
	}
	return out, nil
}

func (c *Client) AIBudgets(ctx context.Context, q AIBudgetsQuery) (AIBudgetsResponse, error) {
	params := url.Values{}
	if q.Provider != "" {
		params.Set("provider", q.Provider)
	}
	if q.Model != "" {
		params.Set("model", q.Model)
	}
	if q.SessionID != "" {
		params.Set("session_id", q.SessionID)
	}
	if q.CallerID != "" {
		params.Set("caller_id", q.CallerID)
	}
	var out AIBudgetsResponse
	if err := c.getJSON(ctx, withQuery("/ai/budgets", params), &out); err != nil {
		return AIBudgetsResponse{}, err
	}
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	return c.doJSON(ctx, http.MethodGet, path, nil, http.StatusOK, out)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, wantStatus int, out any) error {
	return c.doJSONWithClient(ctx, c.http, method, path, body, wantStatus, out)
}

func (c *Client) doJSONWithClient(ctx context.Context, httpClient *http.Client, method, path string, body any, wantStatus int, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return wrapIfUnreachable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		return readAPIError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return nil
}

func (c *Client) doNoBody(ctx context.Context, method, path string, body any, wantStatus int) error {
	return c.doJSON(ctx, method, path, body, wantStatus, nil)
}

func (c *Client) doNoBodyWithClient(ctx context.Context, httpClient *http.Client, method, path string, body any, wantStatus int) error {
	return c.doJSONWithClient(ctx, httpClient, method, path, body, wantStatus, nil)
}

func (c *Client) doJSONStream(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.longLivedClient().Do(req)
	if err != nil {
		return nil, wrapIfUnreachable(err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}
	return resp, nil
}

func (c *Client) doStream(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+withQuery(path, q), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.longLivedClient().Do(req)
	if err != nil {
		return nil, wrapIfUnreachable(err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}
	return resp, nil
}

func (c *Client) longLivedClient() *http.Client {
	longClient := *c.http
	longClient.Timeout = 0
	return &longClient
}

func transportFor(listenAddr string) (string, *http.Client, error) {
	const timeout = 5 * time.Second
	switch {
	case strings.HasPrefix(listenAddr, "unix:"):
		socketPath := strings.TrimPrefix(listenAddr, "unix:")
		if socketPath == "" {
			return "", nil, fmt.Errorf("unix listen addr missing path")
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		}
		return "http://unix", &http.Client{Transport: transport, Timeout: timeout}, nil
	case strings.HasPrefix(listenAddr, "tcp:"):
		hostPort := strings.TrimPrefix(listenAddr, "tcp:")
		if hostPort == "" {
			return "", nil, fmt.Errorf("tcp listen addr missing host:port")
		}
		return "http://" + hostPort, &http.Client{Timeout: timeout}, nil
	case strings.HasPrefix(listenAddr, "http://") || strings.HasPrefix(listenAddr, "https://"):
		return strings.TrimRight(listenAddr, "/"), &http.Client{Timeout: timeout}, nil
	default:
		return "", nil, fmt.Errorf("unsupported listen addr %q (expect unix:/path, tcp:host:port, or http(s)://host)", listenAddr)
	}
}

func expandListenAddr(addr string) string {
	if !strings.HasPrefix(addr, "unix:~/") {
		return addr
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return addr
	}
	return "unix:" + filepath.Join(home, strings.TrimPrefix(addr, "unix:~/"))
}

func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

func decodeJSON(r io.Reader, out any) error {
	return json.NewDecoder(r).Decode(out)
}
