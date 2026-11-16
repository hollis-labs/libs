package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/ui/ui-go/ssekit"
)

// gateway is the built-in model client: the Anthropic Messages API, streamed, at
// <BaseURL>/v1/messages with a bearer key. It is an llmcontracts.Provider.
type gateway struct {
	cfg    Config
	http   *http.Client
	images *imageStore
}

// newGateway returns the client. It makes no request and reads no key until a turn runs.
func newGateway(cfg Config, images *imageStore) *gateway {
	// No overall timeout: a stream lives as long as the model writes. The turn's idle
	// watchdog (agent/core/turn) ends one that goes quiet.
	return &gateway{cfg: cfg, images: images, http: &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}}}
}

type sessionKey struct{}

// withSession names the conversation a request belongs to (Config.SessionHeader).
func withSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionKey{}, id)
}

// key reads the bearer key from its file on every request, so a rotated key is picked up
// without a restart. The key is never logged and never leaves this function except in the
// Authorization header.
func (g *gateway) key() (string, error) {
	data, err := os.ReadFile(g.cfg.KeyFile)
	if err != nil {
		return "", fmt.Errorf("the model key file could not be read: %w", err)
	}
	k := strings.TrimSpace(string(data))
	if k == "" {
		return "", errors.New("the model key file is empty")
	}
	return k, nil
}

// The request body, as the Messages API takes it.
type wireRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    []wireBlock   `json:"system,omitempty"`
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools,omitempty"`
	Stream    bool          `json:"stream"`
}
type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}
type wireBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     *map[string]any `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	// Content is a tool result: its text, or its text and pictures as blocks (see images.go).
	Content any         `json:"content,omitempty"`
	Source  *wireSource `json:"source,omitempty"`
	IsError bool        `json:"is_error,omitempty"`
	// CacheControl marks the end of a prefix the provider may cache (see cached).
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// wireSource is a picture in an image block.
type wireSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type cacheControl struct {
	Type string `json:"type"`
}

// cached is the marker for "everything up to here is the same next time". A request is
// tools, then the system prompt, then the messages; each turn repeats the one before it
// and adds to the end. Marking the last tool, the system prompt and the last message lets
// the provider read the repeated part from its cache at a fraction of the price instead
// of processing the whole thread again on every step.
var cached = &cacheControl{Type: "ephemeral"}

type wireTool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	InputSchema  map[string]any `json:"input_schema"`
	CacheControl *cacheControl  `json:"cache_control,omitempty"`
}

func wireMessages(in []llmtypes.ChatMessage, images *imageStore) []wireMessage {
	out := make([]wireMessage, 0, len(in))
	seen := images.shown(in)
	for _, m := range in {
		w := wireMessage{Role: m.Role}
		if len(m.ContentBlocks) == 0 {
			if m.Content == "" {
				continue
			}
			w.Content = []wireBlock{{Type: "text", Text: m.Content}}
		}
		for _, b := range m.ContentBlocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					w.Content = append(w.Content, wireBlock{Type: "text", Text: b.Text})
				}
			case "tool_use":
				input := map[string]any{}
				if b.Input != nil && *b.Input != nil {
					input = *b.Input
				}
				w.Content = append(w.Content, wireBlock{Type: "tool_use", ID: b.ID, Name: b.Name, Input: &input})
			case "tool_result":
				w.Content = append(w.Content, wireBlock{Type: "tool_result", ToolUseID: b.ToolUseID, Content: resultContent(b.Content, seen[b.ToolUseID]), IsError: b.IsError})
			}
			// Thinking blocks are not kept: this client does not ask for extended thinking.
		}
		if len(w.Content) > 0 {
			out = append(out, w)
		}
	}
	return out
}

func (g *gateway) request(ctx context.Context, req llmtypes.ChatRequest, stream bool) (*http.Response, error) {
	if g.cfg.BaseURL == "" || g.cfg.KeyFile == "" {
		return nil, errors.New("no model gateway is configured")
	}
	key, err := g.key()
	if err != nil {
		return nil, err
	}
	body := wireRequest{Model: req.Model, MaxTokens: req.MaxTokens, Messages: wireMessages(req.Messages, g.images), Stream: stream}
	if sys := req.EffectiveSystemPrompt(); sys != "" {
		body.System = []wireBlock{{Type: "text", Text: sys, CacheControl: cached}}
	}
	if n := len(body.Messages); n > 0 && stream {
		last := body.Messages[n-1].Content
		last[len(last)-1].CacheControl = cached
	}
	if body.Model == "" {
		body.Model = g.cfg.Model
	}
	if body.MaxTokens <= 0 {
		body.MaxTokens = g.cfg.MaxTokens
	}
	for _, t := range req.Tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		body.Tools = append(body.Tools, wireTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	if n := len(body.Tools); n > 0 {
		body.Tools[n-1].CacheControl = cached
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.cfg.BaseURL, "/")+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+key)
	hr.Header.Set("anthropic-version", "2023-06-01")
	if stream {
		hr.Header.Set("Accept", "text/event-stream")
	}
	if s, _ := ctx.Value(sessionKey{}).(string); s != "" && g.cfg.SessionHeader != "" {
		hr.Header.Set(g.cfg.SessionHeader, s)
	}
	res, err := g.http.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("the model gateway could not be reached: %w", err)
	}
	if res.StatusCode/100 != 2 {
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
		return nil, &GatewayError{Status: res.StatusCode, Message: errorMessage(raw)}
	}
	return res, nil
}

// GatewayError is a refusal from the gateway or the provider behind it.
type GatewayError struct {
	Status  int
	Message string
}

func (e *GatewayError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the model gateway answered %d", e.Status)
	}
	return fmt.Sprintf("the model gateway answered %d: %s", e.Status, e.Message)
}

// Retryable says whether trying the same request again can help.
func (e *GatewayError) Retryable() bool {
	return e.Status == 408 || e.Status == 429 || e.Status >= 500
}

// errorMessage reads either error shape: the gateway's flat {code, message} or the
// provider's {error: {type, message}}.
func errorMessage(raw []byte) string {
	var v struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &v) == nil {
		if v.Error.Message != "" {
			return v.Error.Message
		}
		if v.Message != "" {
			return v.Message
		}
	}
	return strings.TrimSpace(string(bytes.ToValidUTF8(raw[:min(len(raw), 300)], nil)))
}

// StreamChat starts one model invocation. The channel carries text deltas, each finished
// tool call, usage, and ends with a done or an error event.
func (g *gateway) StreamChat(ctx context.Context, req llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
	res, err := g.request(ctx, req, true)
	if err != nil {
		return nil, err
	}
	out := make(chan llmtypes.StreamEvent, 32)
	go func() {
		defer close(out)
		defer res.Body.Close()
		send := func(ev llmtypes.StreamEvent) bool {
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		type block struct {
			kind, id, name string
			json           strings.Builder
		}
		blocks := map[int]*block{}
		usage := llmtypes.Usage{}
		for ev, err := range ssekit.Read(res.Body) {
			if err != nil {
				send(llmtypes.StreamEvent{Type: llmtypes.EventError, Error: "the model stream broke: " + err.Error()})
				return
			}
			var f struct {
				Type    string `json:"type"`
				Index   int    `json:"index"`
				Message struct {
					Usage wireUsage `json:"usage"`
				} `json:"message"`
				ContentBlock struct {
					Type, ID, Name string
				} `json:"content_block"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
					StopReason  string `json:"stop_reason"`
				} `json:"delta"`
				Usage wireUsage `json:"usage"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(ev.Data, &f) != nil {
				continue
			}
			switch f.Type {
			case "message_start":
				usage.InputTokens = f.Message.Usage.Input
				usage.CacheCreationTokens = f.Message.Usage.CacheWrite
				usage.CacheReadTokens = f.Message.Usage.CacheRead
			case "content_block_start":
				blocks[f.Index] = &block{kind: f.ContentBlock.Type, id: f.ContentBlock.ID, name: f.ContentBlock.Name}
			case "content_block_delta":
				switch f.Delta.Type {
				case "text_delta":
					if f.Delta.Text != "" && !send(llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: f.Delta.Text}) {
						return
					}
				case "input_json_delta":
					if b := blocks[f.Index]; b != nil {
						b.json.WriteString(f.Delta.PartialJSON)
					}
				}
			case "content_block_stop":
				b := blocks[f.Index]
				if b == nil || b.kind != "tool_use" {
					continue
				}
				input := map[string]any{}
				if s := strings.TrimSpace(b.json.String()); s != "" {
					if json.Unmarshal([]byte(s), &input) != nil {
						send(llmtypes.StreamEvent{Type: llmtypes.EventError, Error: "the model sent a tool call that is not valid JSON"})
						return
					}
				}
				if !send(llmtypes.StreamEvent{Type: llmtypes.EventToolUse, ToolUse: &llmtypes.ToolUseBlock{ID: b.id, Name: b.name, Input: input}}) {
					return
				}
			case "message_delta":
				usage.OutputTokens = f.Usage.Output
				if f.Delta.StopReason != "" {
					usage.StopReason = llmtypes.NormalizeStopReason(f.Delta.StopReason)
				}
			case "message_stop":
				u := usage
				if !send(llmtypes.StreamEvent{Type: llmtypes.EventUsage, Usage: &u}) {
					return
				}
				send(llmtypes.StreamEvent{Type: llmtypes.EventDone})
				return
			case "error":
				send(llmtypes.StreamEvent{Type: llmtypes.EventError, Error: f.Error.Message})
				return
			}
		}
		// The body ended without message_stop: the turn reports it as truncated.
	}()
	return out, nil
}

type wireUsage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
}

// Complete is one invocation without streaming, for short internal uses (a title, a summary).
func (g *gateway) Complete(ctx context.Context, req llmtypes.ChatRequest) (string, error) {
	res, err := g.request(ctx, req, false)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var v struct {
		Content []struct {
			Type, Text string
		} `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&v); err != nil {
		return "", fmt.Errorf("the model gateway's answer could not be read: %w", err)
	}
	var b strings.Builder
	for _, c := range v.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String(), nil
}

// Capabilities describes this client, not the model behind an alias: the gateway decides that.
func (g *gateway) Capabilities() llmtypes.ProviderCapabilities {
	return llmtypes.ProviderCapabilities{SupportsToolCalling: true, MaxTokens: g.cfg.MaxTokens, ContextWindowSize: 200_000}
}
