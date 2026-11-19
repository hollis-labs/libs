// Package embed turns texts into vectors through an AI gateway's embeddings
// operation (POST /ai/embeddings). Client satisfies classify.Embedder, so a
// classify.Centroid judge can run on a gateway's embedding model with no local
// model and no cgo.
//
// The wire shape:
//
//	POST {base}/ai/embeddings
//	{"request": {"operation": "embedding", "caller_id", "model_hint", "embedding_input": ["…"]}}
//	200 {"response": {"provider", "model", "embeddings": [{"index", "vector"}], "usage"}}
//	err {"error": {"code", "message"}}
//
// It never puts the token or the input text in an error.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client embeds through a gateway. Safe for concurrent use.
type Client struct {
	BaseURL  string
	Token    string
	CallerID string
	// Model is the model hint; empty lets the gateway choose. Vectors from
	// different models are not comparable, so pin one.
	Model string
	HTTP  *http.Client
	// MaxBatch is how many texts go in one request; 0 is 64.
	MaxBatch int

	mu  sync.Mutex
	dim int // the dimension seen first; a change is an error
}

// NewClient returns a client for baseURL (http(s)://host[:port] or
// unix:///absolute/path.sock), or nil when baseURL is empty.
func NewClient(baseURL, token, callerID, model string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	if sock, ok := strings.CutPrefix(baseURL, "unix://"); ok {
		hc.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}
		baseURL = "http://unix"
	}
	return &Client{BaseURL: baseURL, Token: token, CallerID: callerID, Model: model, HTTP: hc}
}

// Name is the embedder's name with its model, so a centroid fitted on one model
// is not mistaken for another's.
func (c *Client) Name() string { return "embed-http:" + c.Model }

// Error is a failed call. Message is the gateway's own text.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("embed: HTTP %d %s", e.Status, e.Code)
	}
	return fmt.Sprintf("embed: HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

type wireRequest struct {
	Request struct {
		Operation      string   `json:"operation"`
		CallerID       string   `json:"caller_id,omitempty"`
		ModelHint      string   `json:"model_hint,omitempty"`
		EmbeddingInput []string `json:"embedding_input"`
	} `json:"request"`
}

type wireResponse struct {
	Response struct {
		Model      string `json:"model"`
		Embeddings []struct {
			Index  int       `json:"index"`
			Vector []float32 `json:"vector"`
		} `json:"embeddings"`
	} `json:"response"`
}

// Embed returns one vector per text, in order.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	batch := c.MaxBatch
	if batch <= 0 {
		batch = 64
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batch {
		end := min(start+batch, len(texts))
		vecs, err := c.once(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (c *Client) once(ctx context.Context, texts []string) ([][]float32, error) {
	var w wireRequest
	w.Request.Operation = "embedding"
	w.Request.CallerID = c.CallerID
	w.Request.ModelHint = c.Model
	w.Request.EmbeddingInput = texts
	body, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/ai/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		hr.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("embed: %s", c.redact(err.Error()))
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if res.StatusCode != http.StatusOK {
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &env)
		msg := env.Error.Message
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, &Error{Status: res.StatusCode, Code: env.Error.Code, Message: c.redact(msg)}
	}
	var wr wireResponse
	if err := json.Unmarshal(raw, &wr); err != nil {
		return nil, fmt.Errorf("embed: bad response: %w", err)
	}
	if len(wr.Response.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embed: %d vectors for %d texts", len(wr.Response.Embeddings), len(texts))
	}
	out := make([][]float32, len(texts))
	for _, e := range wr.Response.Embeddings {
		if e.Index < 0 || e.Index >= len(texts) || out[e.Index] != nil {
			return nil, errors.New("embed: vector indexes out of range or repeated")
		}
		if len(e.Vector) == 0 {
			return nil, errors.New("embed: empty vector")
		}
		out[e.Index] = e.Vector
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, v := range out {
		if c.dim == 0 {
			c.dim = len(v)
		}
		if len(v) != c.dim {
			return nil, fmt.Errorf("embed: vector dimension changed from %d to %d (has the model changed?)", c.dim, len(v))
		}
	}
	return out, nil
}

func (c *Client) redact(s string) string {
	if len(c.Token) >= 4 {
		s = strings.ReplaceAll(s, c.Token, "[redacted]")
	}
	return s
}
