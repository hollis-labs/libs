package tether

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewUnixExpandsDefault(t *testing.T) {
	c, err := New("")
	if err != nil {
		t.Fatalf("New default: %v", err)
	}
	if c.baseURL != "http://unix" {
		t.Fatalf("baseURL = %q, want http://unix", c.baseURL)
	}
}

func TestHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/health" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(Health{Status: "ok", PID: 42})
	}))
	defer srv.Close()

	c := MustNew(srv.URL)
	got, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got.Status != "ok" || got.PID != 42 {
		t.Fatalf("Health = %+v", got)
	}
}

func TestCreateEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/broker/envelopes" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var req EnvelopeCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Sender != "nanite" || req.Recipient != "clockwork" {
			t.Fatalf("request = %+v", req)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Envelope{ID: "env-1", Sender: req.Sender, Recipient: req.Recipient})
	}))
	defer srv.Close()

	c := MustNew(srv.URL)
	got, err := c.CreateEnvelope(context.Background(), EnvelopeCreateRequest{
		Sender:    "nanite",
		Recipient: "clockwork",
	})
	if err != nil {
		t.Fatalf("CreateEnvelope: %v", err)
	}
	if got.ID != "env-1" {
		t.Fatalf("Envelope ID = %q", got.ID)
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Error: ErrorDetail{
			Code:    ErrorCodeConflict,
			Message: "wrong state",
		}})
	}))
	defer srv.Close()

	c := MustNew(srv.URL)
	_, err := c.GetSession(context.Background(), "s1")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusConflict || apiErr.Code != ErrorCodeConflict {
		t.Fatalf("APIError = %+v", apiErr)
	}
}

func TestAPIErrorFormatting(t *testing.T) {
	tests := []struct {
		err  *APIError
		want string
	}{
		{&APIError{StatusCode: 400, Code: "invalid_request", Message: "bad body"}, "tether 400 (invalid_request): bad body"},
		{&APIError{StatusCode: 404, Code: "not_found"}, "tether 404 (not_found)"},
		{&APIError{StatusCode: 500, Message: "boom"}, "tether 500: boom"},
		{&APIError{StatusCode: 502, Body: "proxy error"}, "tether 502: proxy error"},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Fatalf("Error() = %q, want %q", got, tt.want)
		}
	}
}

func TestSessionLifecycleMethods(t *testing.T) {
	var inputBody []byte
	requests := make([]string, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sessions":
			var req LaunchRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode launch request: %v", err)
			}
			if req.Launch != "nanite-host" {
				t.Fatalf("launch = %q", req.Launch)
			}
			w.WriteHeader(http.StatusCreated)
			writeTestJSON(t, w, LaunchResponse{ID: "s1", Workspace: "/tmp/ws", Log: "/tmp/log"})
		case r.Method == http.MethodPost && r.URL.Path == "/sessions/s1/launch":
			writeTestJSON(t, w, LaunchResponse{ID: "s1", Workspace: "/tmp/ws", Log: "/tmp/log"})
		case r.Method == http.MethodGet && r.URL.Path == "/sessions":
			if got := r.URL.Query(); got.Get("limit") != "2" || got.Get("cursor") != "cur" || got.Get("state") != "running" {
				t.Fatalf("query = %s", got.Encode())
			}
			writeTestJSON(t, w, ListSessionsResponse{Sessions: []Session{{ID: "s1"}}, NextCursor: "next"})
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/s1":
			writeTestJSON(t, w, Session{ID: "s1", State: "running"})
		case r.Method == http.MethodPost && r.URL.Path == "/sessions/s1/resize":
			var req ResizeRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode resize request: %v", err)
			}
			if req.Rows != 42 || req.Cols != 120 {
				t.Fatalf("resize = %+v", req)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/sessions/s1/input":
			var err error
			inputBody, err = io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read input: %v", err)
			}
			if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
				t.Fatalf("content type = %q", got)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/s1/attach":
			if r.URL.Query().Get("since_seq") != "9" {
				t.Fatalf("since_seq = %q", r.URL.Query().Get("since_seq"))
			}
			_, _ = w.Write([]byte("attached"))
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/s1/wait":
			writeTestJSON(t, w, WaitResponse{ExitCode: 7})
		case r.Method == http.MethodPost && r.URL.Path == "/sessions/s1/stop":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer srv.Close()

	c := MustNew(srv.URL)
	ctx := context.Background()
	if got, err := c.CreateSession(ctx, "nanite-host"); err != nil || got.ID != "s1" {
		t.Fatalf("CreateSession = %+v, %v", got, err)
	}
	if got, err := c.LaunchSession(ctx, "s1"); err != nil || got.ID != "s1" {
		t.Fatalf("LaunchSession = %+v, %v", got, err)
	}
	if got, err := c.ListSessions(ctx, ListSessionsOptions{Limit: 2, Cursor: "cur", State: "running"}); err != nil || got.NextCursor != "next" {
		t.Fatalf("ListSessions = %+v, %v", got, err)
	}
	if got, err := c.GetSession(ctx, "s1"); err != nil || got.State != "running" {
		t.Fatalf("GetSession = %+v, %v", got, err)
	}
	if err := c.ResizeSession(ctx, "s1", 42, 120); err != nil {
		t.Fatalf("ResizeSession: %v", err)
	}
	if err := c.SendInput(ctx, "s1", []byte("hello\n")); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if string(inputBody) != "hello\n" {
		t.Fatalf("input body = %q", inputBody)
	}
	var attached bytes.Buffer
	if err := c.AttachSession(ctx, "s1", &attached, 9); err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	if attached.String() != "attached" {
		t.Fatalf("attached = %q", attached.String())
	}
	if got, err := c.WaitSession(ctx, "s1"); err != nil || got != 7 {
		t.Fatalf("WaitSession = %d, %v", got, err)
	}
	if err := c.StopSession(ctx, "s1"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if len(requests) != 9 {
		t.Fatalf("requests = %v", requests)
	}
}

func TestCheckpointBrokerEventAndCatalogMethods(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sessions/s1/checkpoint":
			var req CheckpointCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode checkpoint: %v", err)
			}
			writeStatusJSON(t, w, http.StatusCreated, Checkpoint{ID: "cp1", Summary: req.Summary})
		case r.Method == http.MethodGet && r.URL.Path == "/logical-agents/nanite/checkpoints":
			writeTestJSON(t, w, CheckpointListResponse{Checkpoints: []Checkpoint{{ID: "cp1"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/broker/envelopes":
			q := r.URL.Query()
			if q.Get("workflow_id") == "wf1" && q.Get("correlation_id") == "corr1" {
				writeTestJSON(t, w, EnvelopeListResponse{Envelopes: []Envelope{{ID: "env-workflow"}}})
				return
			}
			if q.Get("recipient") == "nanite" {
				writeTestJSON(t, w, EnvelopeListResponse{Envelopes: []Envelope{{ID: "env-recipient"}}})
				return
			}
			t.Fatalf("unexpected envelope query %s", q.Encode())
		case r.Method == http.MethodGet && r.URL.Path == "/broker/envelopes/env1":
			writeTestJSON(t, w, Envelope{ID: "env1"})
		case r.Method == http.MethodPost && r.URL.Path == "/broker/envelopes/env1/reply":
			writeStatusJSON(t, w, http.StatusCreated, Envelope{ID: "reply1"})
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/s1/events":
			if q := r.URL.Query(); q.Get("limit") != "3" || q.Get("cursor") != "44" {
				t.Fatalf("events query = %s", q.Encode())
			}
			writeTestJSON(t, w, EventListResponse{Events: []Event{{Seq: 45}}, NextCursor: 44})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/catalog/"):
			writeCatalogResponse(t, w, r.URL.Path)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer srv.Close()

	c := MustNew(srv.URL)
	ctx := context.Background()
	if got, err := c.CreateCheckpoint(ctx, "s1", CheckpointCreateRequest{Summary: "saved"}); err != nil || got.ID != "cp1" {
		t.Fatalf("CreateCheckpoint = %+v, %v", got, err)
	}
	if got, err := c.ListCheckpoints(ctx, "nanite"); err != nil || len(got.Checkpoints) != 1 {
		t.Fatalf("ListCheckpoints = %+v, %v", got, err)
	}
	if got, err := c.ListEnvelopes(ctx, EnvelopeListOptions{Recipient: "nanite"}); err != nil || got.Envelopes[0].ID != "env-recipient" {
		t.Fatalf("ListEnvelopes recipient = %+v, %v", got, err)
	}
	if got, err := c.ListEnvelopes(ctx, EnvelopeListOptions{WorkflowID: "wf1", CorrelationID: "corr1"}); err != nil || got.Envelopes[0].ID != "env-workflow" {
		t.Fatalf("ListEnvelopes workflow = %+v, %v", got, err)
	}
	if got, err := c.GetEnvelope(ctx, "env1"); err != nil || got.ID != "env1" {
		t.Fatalf("GetEnvelope = %+v, %v", got, err)
	}
	if got, err := c.ReplyEnvelope(ctx, "env1", EnvelopeCreateRequest{Payload: "{}"}); err != nil || got.ID != "reply1" {
		t.Fatalf("ReplyEnvelope = %+v, %v", got, err)
	}
	if got, err := c.ListSessionEvents(ctx, "s1", EventListOptions{Limit: 3, Cursor: 44}); err != nil || got.NextCursor != 44 {
		t.Fatalf("ListSessionEvents = %+v, %v", got, err)
	}
	if got, err := c.ListProjects(ctx); err != nil || got[0].ID != "demo" {
		t.Fatalf("ListProjects = %+v, %v", got, err)
	}
	if got, err := c.ListAgents(ctx); err != nil || got[0].ID != "nanite" {
		t.Fatalf("ListAgents = %+v, %v", got, err)
	}
	if got, err := c.ListProviders(ctx); err != nil || got[0].ID != "api" {
		t.Fatalf("ListProviders = %+v, %v", got, err)
	}
	if got, err := c.ListLaunches(ctx); err != nil || got[0].ID != "nanite-host" {
		t.Fatalf("ListLaunches = %+v, %v", got, err)
	}
}

func TestStreamEventsParsesSSEAndExitsOnCancel(t *testing.T) {
	flushed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events/stream" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		wantQuery := url.Values{"scope": {"session", "broker"}, "session_id": {"s1"}, "since_seq": {"10"}}
		if !reflect.DeepEqual(r.URL.Query(), wantQuery) {
			t.Fatalf("query = %v, want %v", r.URL.Query(), wantQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
		}
		fmt.Fprint(w, ": ping\n\n")
		fmt.Fprint(w, "id: 11\nevent: session.state_changed\ndata: {\"scope\":\"session\",\"session_id\":\"s1\",\"payload_json\":\"{\\\"to\\\":\\\"running\\\"}\"}\n\n")
		fmt.Fprint(w, "id: 12\nevent: broker.envelope_created\ndata: {\"scope\":\"broker\",\"payload_json\":\"{\\\"id\\\":\\\"env1\\\"}\"}\n\n")
		flusher.Flush()
		close(flushed)
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := MustNew(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	events, errs := c.StreamEvents(ctx, StreamEventsOptions{
		SinceSeq:  10,
		Scopes:    []string{ScopeSession, ScopeBroker},
		SessionID: "s1",
	})
	<-flushed

	first := <-events
	second := <-events
	cancel()

	if first.Seq != 11 || first.Kind != "session.state_changed" || first.Scope != ScopeSession || first.SessionID != "s1" {
		t.Fatalf("first event = %+v", first)
	}
	if second.Seq != 12 || second.Kind != "broker.envelope_created" || second.Scope != ScopeBroker {
		t.Fatalf("second event = %+v", second)
	}
	select {
	case err := <-errs:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("stream error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not exit after cancellation")
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	writeStatusJSON(t, w, http.StatusOK, body)
}

func writeStatusJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func writeCatalogResponse(t *testing.T, w http.ResponseWriter, path string) {
	t.Helper()
	switch path {
	case "/catalog/projects":
		writeTestJSON(t, w, struct {
			Projects []Project `json:"projects"`
		}{Projects: []Project{{ID: "demo"}}})
	case "/catalog/agents":
		writeTestJSON(t, w, struct {
			Agents []Agent `json:"agents"`
		}{Agents: []Agent{{ID: "nanite"}}})
	case "/catalog/providers":
		writeTestJSON(t, w, struct {
			Providers []Provider `json:"providers"`
		}{Providers: []Provider{{ID: "api"}}})
	case "/catalog/launches":
		writeTestJSON(t, w, struct {
			Launches []Launch `json:"launches"`
		}{Launches: []Launch{{ID: "nanite-host"}}})
	default:
		t.Fatalf("unexpected catalog path %s", path)
	}
}
