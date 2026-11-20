package host

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/agent/core/turn"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
)

// A model writing a long tool input sends input_json_delta frames, which the gateway keeps
// until the block ends. Without keepalives the turn's idle timer saw nothing for the whole
// input and gave the turn up as stalled while the model was still writing (Desk, 2026-11-20:
// "turn stream stalled: no provider events for 1m30s" on a long drafts_write).
func TestLongToolInputIsNotAStall(t *testing.T) {
	old := keepaliveEvery
	keepaliveEvery = 20 * time.Millisecond
	t.Cleanup(func() { keepaliveEvery = old })

	const chunks, gap = 12, 40 * time.Millisecond // 480ms of tool input, far past the idle timeout
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		write := func(e string) { fmt.Fprint(w, sse(e)); fl.Flush() }
		write(`{"type":"message_start","message":{"usage":{"input_tokens":10}}}`)
		write(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"drafts_write"}}`)
		write(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"text\":\""}}`)
		for range chunks {
			time.Sleep(gap)
			write(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"lorem ipsum "}}`)
		}
		write(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"}"}}`)
		write(`{"type":"content_block_stop","index":0}`)
		write(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":500}}`)
		write(`{"type":"message_stop"}`)
	}))
	defer srv.Close()
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := newGateway(Config{BaseURL: srv.URL, KeyFile: key, Model: "m"}, nil)

	var tools []llmtypes.ToolUseBlock
	_, err := turn.ExecuteTurn(context.Background(), turn.TurnRequest{
		IdleTimeout:     150 * time.Millisecond,
		RequireTerminal: true,
		Stream: func(ctx context.Context) (<-chan llmtypes.StreamEvent, error) {
			return g.StreamChat(ctx, llmtypes.ChatRequest{Model: "m", MaxTokens: 1000, Messages: []llmtypes.ChatMessage{{Role: "user", Content: "write it"}}})
		},
		Sink: turn.TurnSink{OnToolUse: func(tu llmtypes.ToolUseBlock) { tools = append(tools, tu) }},
	})
	if err != nil {
		t.Fatalf("a long tool input was taken for a stall: %v", err)
	}
	if len(tools) != 1 || !strings.Contains(fmt.Sprint(tools[0].Input["text"]), strings.Repeat("lorem ipsum ", chunks)) {
		t.Fatalf("the tool call did not come through whole: %+v", tools)
	}
}

// A stream that truly goes quiet is still a stall: keepalives come only from frames.
func TestQuietStreamStillStalls(t *testing.T) {
	old := keepaliveEvery
	keepaliveEvery = 20 * time.Millisecond
	t.Cleanup(func() { keepaliveEvery = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(`{"type":"message_start","message":{"usage":{"input_tokens":10}}}`))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("k"), 0o600)
	g := newGateway(Config{BaseURL: srv.URL, KeyFile: key, Model: "m"}, nil)
	_, err := turn.ExecuteTurn(context.Background(), turn.TurnRequest{
		IdleTimeout:     150 * time.Millisecond,
		RequireTerminal: true,
		Stream: func(ctx context.Context) (<-chan llmtypes.StreamEvent, error) {
			return g.StreamChat(ctx, llmtypes.ChatRequest{Model: "m", MaxTokens: 1000, Messages: []llmtypes.ChatMessage{{Role: "user", Content: "hi"}}})
		},
	})
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("a quiet stream was not a stall: %v", err)
	}
}
