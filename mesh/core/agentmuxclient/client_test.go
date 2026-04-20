package agentmux

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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
