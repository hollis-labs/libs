package decide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is a Decider over HTTP, in one of two dialects (Dialect).
//
// DialectJEV is JEV's own System-1 API, as an LLM gateway relays it:
//
//	POST {base}/v1/systemone
//	{"state", "model", "questions": {"<key>": {"type", "instructions", "criteria"}}}
//	200 {"model", "answers": {"<key>": {...}}, "usage"}
//
// Questions are an object; their order, and the order of options and levels, is
// written exactly as given (JEV leans toward the first option).
//
// DialectGateway is a decision gateway's provider-neutral operation:
//
//	POST {base}/ai/decide
//	{"request": {"caller_id", "model_hint", "decision": {"state", "purpose", "on_error", "questions"}}}
//	200 {"response": {"provider", "model", "decision": {"answers", "latency_ms", "request_id"}, "usage"}}
//	err {"error": {"code", "message", "details": {"kind"}}}
//
// It never logs, prints or returns the token, and never puts request text in an
// error.
type Client struct {
	// Dialect is the wire; empty is DialectGateway.
	Dialect  Dialect
	BaseURL  string
	Token    string
	CallerID string
	HTTP     *http.Client
	// Backoff is the wait before the first retry, doubling after each (default
	// 500ms). Only rate-limited, overloaded and timed-out calls are retried.
	Backoff  time.Duration
	Attempts int // default 3
}

// Dialect is a Client's wire.
type Dialect string

const (
	// DialectGateway is POST /ai/decide with provider-neutral questions.
	DialectGateway Dialect = "gateway"
	// DialectJEV is POST /v1/systemone with JEV's own body (an LLM gateway
	// relays it). Request.Model is required: the gateway routes on it ("jev").
	DialectJEV Dialect = "jev"
)

// NewJEVClient returns a client for JEV's System-1 API at baseURL (an LLM
// gateway's base, which serves /v1/systemone), or nil when baseURL is empty.
func NewJEVClient(baseURL, token string) *Client {
	c := NewClient(baseURL, token, "")
	if c != nil {
		c.Dialect = DialectJEV
	}
	return c
}

// NewClient returns a client for baseURL (http(s)://host[:port] or
// unix:///absolute/path.sock), or nil when baseURL is empty.
func NewClient(baseURL, token, callerID string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	if sock, ok := strings.CutPrefix(baseURL, "unix://"); ok {
		hc.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}
		baseURL = "http://unix" // a placeholder host: the dialer decides where the bytes go
	}
	return &Client{BaseURL: baseURL, Token: token, CallerID: callerID, HTTP: hc}
}

// Name implements Decider.
func (c *Client) Name() string {
	if c.Dialect == DialectJEV {
		return "decide-jev"
	}
	return "decide-http"
}

// Error is a failed call. Message is the gateway's own text, never the request.
type Error struct {
	Status  int
	Code    string // invalid_request, decision_failed, internal_error, ...
	Kind    string // for decision_failed: auth, rate_limited, overloaded, timeout, circuit_open, unavailable
	Message string

	retryAfter time.Duration
}

func (e *Error) Error() string {
	head := fmt.Sprintf("decide: HTTP %d", e.Status)
	if e.Kind != "" {
		head += " (" + e.Kind + ")"
	}
	if e.Message == "" {
		return head
	}
	return head + ": " + e.Message
}

// Retryable: rate limited (429), overloaded (503) or timed out (504); not an
// open breaker, an auth failure or an unavailable provider.
func (e *Error) Retryable() bool {
	if e.Kind == "circuit_open" || e.Kind == "auth" || e.Kind == "unavailable" {
		return false
	}
	switch e.Status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

type wireRequest struct {
	Request struct {
		CallerID  string       `json:"caller_id,omitempty"`
		ModelHint string       `json:"model_hint,omitempty"`
		Decision  wireDecision `json:"decision"`
	} `json:"request"`
}

type wireDecision struct {
	State     any        `json:"state"`
	Purpose   string     `json:"purpose"`
	OnError   string     `json:"on_error,omitempty"`
	Questions []Question `json:"questions"`
}

type wireResponse struct {
	Response struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Decision struct {
			Answers   map[string]Answer `json:"answers"`
			LatencyMS int64             `json:"latency_ms"`
			RequestID string            `json:"request_id"`
		} `json:"decision"`
		Usage Usage `json:"usage"`
	} `json:"response"`
}

func (c *Client) encode(req Request) ([]byte, error) {
	if err := req.Check(); err != nil {
		return nil, err
	}
	if c.Dialect == DialectJEV {
		return encodeJEV(req)
	}
	var w wireRequest
	w.Request.CallerID = req.CallerID
	if w.Request.CallerID == "" {
		w.Request.CallerID = c.CallerID
	}
	w.Request.ModelHint = req.Model
	w.Request.Decision = wireDecision{State: req.State, Purpose: req.Purpose, OnError: req.OnError, Questions: req.Questions}
	if w.Request.Decision.OnError == "" {
		w.Request.Decision.OnError = "fail_open"
	}
	return json.Marshal(w)
}

// decode reads the answers and checks each against its question: present, of
// the asked type, a choice among the options, a probability in range.
func decode(req Request, body []byte) (Response, error) {
	var w wireResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return Response{}, fmt.Errorf("decide: bad response: %w", err)
	}
	r := w.Response
	resp := Response{Provider: r.Provider, Model: r.Model, Usage: r.Usage,
		Latency: time.Duration(r.Decision.LatencyMS) * time.Millisecond, RequestID: r.Decision.RequestID}
	return check(req, resp, r.Decision.Answers)
}

// check keeps one answer per question, each fitting its question.
func check(req Request, resp Response, answers map[string]Answer) (Response, error) {
	resp.Answers = map[string]Answer{}
	for _, q := range req.Questions {
		a, ok := answers[q.Key]
		if !ok {
			return Response{}, fmt.Errorf("decide: no answer for question %q", q.Key)
		}
		if a.Type == "" {
			a.Type = q.Type
		}
		if a.Type != q.Type {
			return Response{}, fmt.Errorf("decide: question %q is %s but the answer is %s", q.Key, q.Type, a.Type)
		}
		for k, p := range a.Probabilities {
			if p < 0 || p > 1 {
				return Response{}, fmt.Errorf("decide: question %q: probability for %q outside 0..1", q.Key, k)
			}
		}
		switch a.Type {
		case Choice:
			if a.Choice == "" {
				return Response{}, fmt.Errorf("decide: question %q has no choice", q.Key)
			}
			known := map[string]bool{}
			for _, o := range q.Options {
				known[o.Key] = true
			}
			if !known[a.Choice] {
				return Response{}, fmt.Errorf("decide: question %q chose an option it was not offered", q.Key)
			}
			if a.Confidence == 0 && len(a.Probabilities) > 0 {
				a.Confidence = ChoiceConfidence(a.Probabilities)
			}
		case Score:
			if a.Confidence == 0 && len(a.Probabilities) > 0 {
				a.Confidence = ScoreConfidence(ScoreLevels(a.Probabilities))
			}
		case Noul:
			if a.Noul < 0 || a.Noul > 1 {
				return Response{}, fmt.Errorf("decide: question %q probability outside 0..1", q.Key)
			}
			if a.Confidence == 0 {
				a.Confidence = NoulConfidence(a.Noul)
			}
		}
		resp.Answers[q.Key] = a
	}
	return resp, nil
}

// Decide implements Decider.
func (c *Client) Decide(ctx context.Context, req Request) (Response, error) {
	body, err := c.encode(req)
	if err != nil {
		return Response{}, err
	}
	attempts, backoff := c.Attempts, c.Backoff
	if attempts < 1 {
		attempts = 3
	}
	if backoff <= 0 {
		backoff = 500 * time.Millisecond
	}
	start := time.Now()
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			wait := backoff << (i - 1)
			var te *Error
			if errors.As(last, &te) && te.retryAfter > 0 {
				wait = te.retryAfter
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return Response{}, ctx.Err()
			}
		}
		resp, err := c.once(ctx, req, body)
		if err == nil {
			if resp.Latency == 0 {
				resp.Latency = time.Since(start)
			}
			return resp, nil
		}
		last = err
		var te *Error
		if !errors.As(err, &te) || !te.Retryable() {
			return Response{}, err
		}
	}
	return Response{}, last
}

func (c *Client) once(ctx context.Context, req Request, body []byte) (Response, error) {
	path := "/ai/decide"
	if c.Dialect == DialectJEV {
		path = "/v1/systemone"
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	if c.Token != "" {
		hr.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(hr)
	if err != nil {
		return Response{}, fmt.Errorf("decide: %s", c.sanitize(err.Error()))
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		e := &Error{Status: res.StatusCode}
		e.Code, e.Kind, e.Message = c.errorFields(raw)
		if s := res.Header.Get("Retry-After"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 10 {
				e.retryAfter = time.Duration(n) * time.Second
			}
		}
		return Response{}, e
	}
	if c.Dialect == DialectJEV {
		return decodeJEV(req, raw)
	}
	return decode(req, raw)
}

func (c *Client) errorFields(raw []byte) (code, kind, msg string) {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Kind string `json:"kind"`
			} `json:"details"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &env) == nil {
		code, kind, msg = env.Error.Code, env.Error.Details.Kind, env.Error.Message
		if msg == "" {
			msg = env.Message
		}
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return code, kind, c.sanitize(msg)
}

func (c *Client) sanitize(s string) string {
	if len(c.Token) >= 4 {
		s = strings.ReplaceAll(s, c.Token, scrubMarker)
	}
	return Scrub(s)
}
