package tether

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAIClientMethods(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ai/providers":
			writeTestJSON(t, w, ListAIProvidersResponse{Providers: []AIProvider{{ID: "anthropic-work", Type: "anthropic", Models: []string{"claude-sonnet-4-5"}}}})
		case "/ai/models":
			if got := r.URL.Query().Get("provider_id"); got != "anthropic-work" {
				t.Fatalf("provider_id = %q", got)
			}
			writeTestJSON(t, w, ListAIModelsResponse{Models: []AIModel{{ConfiguredProviderID: "anthropic-work", ID: "claude-sonnet-4-5"}}})
		case "/ai/routes":
			writeTestJSON(t, w, ListAIRoutesResponse{Routes: []AIRoute{{Provider: "anthropic-work", Model: "claude-sonnet-4-5"}}})
		case "/ai/routes/preview":
			writeTestJSON(t, w, RoutePreviewResponse{Route: AIRoutePreview{Provider: "anthropic-work", Model: "claude-sonnet-4-5"}})
		case "/ai/routes/explain":
			writeTestJSON(t, w, RouteExplainResponse{
				PolicyVersion: "v1",
				Winner:        &AIRoutePreview{Provider: "anthropic-work", Model: "claude-sonnet-4-5"},
				Candidates:    []AIRouteExplainCandidate{{Provider: "anthropic-work", Model: "claude-sonnet-4-5", Matched: true, Selected: true}},
			})
		case "/ai/chat":
			writeTestJSON(t, w, ChatResponse{Response: AIResponse{Provider: "anthropic-work", Model: "claude-sonnet-4-5", Output: []AIMessage{{Role: "assistant"}}}})
		case "/ai/audit":
			if got := r.URL.Query(); got.Get("provider") != "anthropic-work" || got.Get("errors_only") != "true" || got.Get("limit") != "5" {
				t.Fatalf("audit query = %s", got.Encode())
			}
			writeTestJSON(t, w, AIAuditResponse{Events: []AIAuditEvent{{ID: 1, EventType: "budget_rejection"}}, Count: 1})
		case "/ai/usage":
			if got := r.URL.Query(); got.Get("provider") != "anthropic-work" || got.Get("operation") != "chat" {
				t.Fatalf("usage query = %s", got.Encode())
			}
			writeTestJSON(t, w, AIUsageResponse{Requests: 3, ByProvider: []AIUsageBreakdown{{Key: "anthropic-work"}}})
		case "/ai/budgets":
			if got := r.URL.Query(); got.Get("caller_id") != "agent-1" {
				t.Fatalf("budgets query = %s", got.Encode())
			}
			writeTestJSON(t, w, AIBudgetsResponse{Budgets: []AIUsageBudgetEntry{{Provider: "anthropic-work", Model: "claude-sonnet-4-5"}}, Count: 1})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := MustNew(srv.URL)
	ctx := context.Background()

	if got, err := c.ListAIProviders(ctx); err != nil || got.Providers[0].ID != "anthropic-work" {
		t.Fatalf("ListAIProviders = %+v, %v", got, err)
	}
	if got, err := c.ListAIModels(ctx, "anthropic-work"); err != nil || got.Models[0].ID != "claude-sonnet-4-5" {
		t.Fatalf("ListAIModels = %+v, %v", got, err)
	}
	if got, err := c.ListAIRoutes(ctx); err != nil || got.Routes[0].Provider != "anthropic-work" {
		t.Fatalf("ListAIRoutes = %+v, %v", got, err)
	}
	req := ChatRequest{Request: AIRequest{Operation: "chat", Input: []AIMessage{{Role: "user", Parts: []AIContentPart{{Type: "text", Text: "hi"}}}}}}
	if got, err := c.PreviewAIRoute(ctx, req); err != nil || got.Route.Model != "claude-sonnet-4-5" {
		t.Fatalf("PreviewAIRoute = %+v, %v", got, err)
	}
	if got, err := c.ExplainAIRoute(ctx, req); err != nil || got.PolicyVersion != "v1" {
		t.Fatalf("ExplainAIRoute = %+v, %v", got, err)
	}
	if got, err := c.AIChat(ctx, req); err != nil || got.Response.Provider != "anthropic-work" {
		t.Fatalf("AIChat = %+v, %v", got, err)
	}
	if got, err := c.AIAudit(ctx, AIAuditQuery{Provider: "anthropic-work", ErrorsOnly: true, Limit: 5}); err != nil || got.Count != 1 {
		t.Fatalf("AIAudit = %+v, %v", got, err)
	}
	if got, err := c.AIUsage(ctx, AIUsageQuery{Provider: "anthropic-work", Operation: "chat"}); err != nil || got.Requests != 3 {
		t.Fatalf("AIUsage = %+v, %v", got, err)
	}
	if got, err := c.AIBudgets(ctx, AIBudgetsQuery{CallerID: "agent-1"}); err != nil || got.Count != 1 {
		t.Fatalf("AIBudgets = %+v, %v", got, err)
	}
}

func TestAIChatStreamParsesEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ai/chat/stream" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"kind\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {\"kind\":\"response.completed\",\"response\":{\"provider\":\"anthropic-work\",\"model\":\"claude-sonnet-4-5\"}}\n\n"))
	}))
	defer srv.Close()

	c := MustNew(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch, errCh, err := c.AIChatStream(ctx, ChatRequest{Request: AIRequest{Operation: "chat"}})
	if err != nil {
		t.Fatalf("AIChatStream: %v", err)
	}

	var got []AIStreamEvent
	for len(got) < 2 {
		select {
		case ev := <-ch:
			got = append(got, ev)
		case err := <-errCh:
			t.Fatalf("stream err = %v", err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for stream events")
		}
	}
	if got[0].Kind != "response.output_text.delta" || got[0].Delta != "hello" {
		t.Fatalf("first event = %+v", got[0])
	}
	if got[1].Kind != "response.completed" || got[1].Response == nil || got[1].Response.Model != "claude-sonnet-4-5" {
		t.Fatalf("second event = %+v", got[1])
	}
}

func TestAIChatStreamPropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"bad request"}}`))
	}))
	defer srv.Close()

	c := MustNew(srv.URL)
	_, _, err := c.AIChatStream(context.Background(), ChatRequest{Request: AIRequest{Operation: "chat"}})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("bad request")) {
		t.Fatalf("err = %v", err)
	}
}

func TestLongLivedAIChatUsesCallerContextNotTransportTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		writeTestJSON(t, w, ChatResponse{Response: AIResponse{Provider: "openai-work", Model: "gpt-5-mini"}})
	}))
	defer srv.Close()

	c := MustNew(srv.URL)
	c.http.Timeout = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := c.AIChat(ctx, ChatRequest{Request: AIRequest{Operation: "chat"}})
	if err != nil {
		t.Fatalf("AIChat: %v", err)
	}
	if got.Response.Model != "gpt-5-mini" {
		t.Fatalf("response = %+v", got.Response)
	}
}

func TestLongLivedAIChatStreamUsesCallerContextNotTransportTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte("event: response.start\n"))
		_, _ = w.Write([]byte("data: {\"kind\":\"response.start\",\"provider\":\"openai-work\",\"model\":\"gpt-5-mini\"}\n\n"))
	}))
	defer srv.Close()

	c := MustNew(srv.URL)
	c.http.Timeout = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ch, errCh, err := c.AIChatStream(ctx, ChatRequest{Request: AIRequest{Operation: "chat"}})
	if err != nil {
		t.Fatalf("AIChatStream: %v", err)
	}
	select {
	case ev := <-ch:
		if ev.Kind != "response.start" {
			t.Fatalf("event kind = %q", ev.Kind)
		}
	case err := <-errCh:
		t.Fatalf("stream err = %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for stream event")
	}
}

func TestDecodeJSONHelper(t *testing.T) {
	var out map[string]any
	if err := decodeJSON(bytes.NewBufferString(`{"ok":true}`), &out); err != nil {
		t.Fatalf("decodeJSON: %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("out = %+v", out)
	}
}
