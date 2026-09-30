package tether

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateSessionWithInputSendsIdempotencyKeyAndAcceptsBothStatuses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		replayed bool
	}{
		{"fresh", http.StatusCreated, false},
		{"replay", http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var raw map[string]any
				if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if raw["idempotency_key"] != "caller/job-1" {
					t.Fatalf("idempotency_key = %v", raw["idempotency_key"])
				}
				writeStatusJSON(t, w, tc.status, LaunchResponse{ID: "s1", Replayed: tc.replayed})
			}))
			defer srv.Close()
			got, err := MustNew(srv.URL).CreateSessionWithInput(context.Background(), LaunchRequest{Launch: "l", IdempotencyKey: "caller/job-1"})
			if err != nil {
				t.Fatalf("CreateSessionWithInput: %v", err)
			}
			if got.ID != "s1" || got.Replayed != tc.replayed {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestCreateSessionOmitsEmptyIdempotencyKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "idempotency_key") {
			t.Fatalf("unkeyed create sent %s", b)
		}
		writeStatusJSON(t, w, http.StatusCreated, LaunchResponse{ID: "s1"})
	}))
	defer srv.Close()
	if _, err := MustNew(srv.URL).CreateSession(context.Background(), "l"); err != nil {
		t.Fatal(err)
	}
}

func TestIdempotencyConflictIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStatusJSON(t, w, http.StatusConflict, ErrorResponse{Error: ErrorDetail{Code: "idempotency_conflict", Message: "key reused"}})
	}))
	defer srv.Close()
	_, err := MustNew(srv.URL).CreateSessionWithInput(context.Background(), LaunchRequest{Launch: "l", IdempotencyKey: "k"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeIdempotencyConflict || apiErr.StatusCode != http.StatusConflict {
		t.Fatalf("err = %#v", err)
	}
	if !errors.Is(err, &APIError{Code: CodeIdempotencyConflict}) {
		t.Fatal("errors.Is on the code failed")
	}
	if CodeProviderSessionLost != "provider_session_lost" {
		t.Fatalf("CodeProviderSessionLost = %q", CodeProviderSessionLost)
	}
}

func TestLaunchSessionReportsReplayed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/s1/launch" {
			t.Fatalf("path %s", r.URL.Path)
		}
		writeStatusJSON(t, w, http.StatusOK, LaunchResponse{ID: "s1", Replayed: true})
	}))
	defer srv.Close()
	got, err := MustNew(srv.URL).LaunchSession(context.Background(), "s1")
	if err != nil || !got.Replayed {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestResumeLogicalAgent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opts     ResumeOptions
		wantBody string
		status   int
		replayed bool
	}{
		{"no options sends no body", ResumeOptions{}, "", http.StatusCreated, false},
		{"keyed fresh", ResumeOptions{IdempotencyKey: "k1"}, `{"idempotency_key":"k1"}`, http.StatusCreated, false},
		{"keyed replay", ResumeOptions{IdempotencyKey: "k1"}, `{"idempotency_key":"k1"}`, http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/logical-agents/la 1/resume" {
					t.Fatalf("request %s %s", r.Method, r.URL.Path)
				}
				b, _ := io.ReadAll(r.Body)
				if strings.TrimSpace(string(b)) != tc.wantBody {
					t.Fatalf("body = %q, want %q", b, tc.wantBody)
				}
				writeStatusJSON(t, w, tc.status, LaunchResponse{ID: "s2", LogicalAgentID: "la 1", Replayed: tc.replayed})
			}))
			defer srv.Close()
			got, err := MustNew(srv.URL).ResumeLogicalAgent(context.Background(), "la 1", tc.opts)
			if err != nil {
				t.Fatalf("ResumeLogicalAgent: %v", err)
			}
			if got.ID != "s2" || got.Replayed != tc.replayed {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestResumeLogicalAgentRefusesEmptyIDAndSurfacesErrors(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		writeStatusJSON(t, w, http.StatusConflict, ErrorResponse{Error: ErrorDetail{Code: "idempotency_conflict", Message: "x"}})
	}))
	defer srv.Close()
	c := MustNew(srv.URL)
	if _, err := c.ResumeLogicalAgent(context.Background(), "", ResumeOptions{}); err == nil || hits != 0 {
		t.Fatalf("empty id: err=%v hits=%d", err, hits)
	}
	if _, err := c.ResumeLogicalAgent(context.Background(), "la", ResumeOptions{IdempotencyKey: "k"}); !errors.Is(err, &APIError{Code: CodeIdempotencyConflict}) {
		t.Fatalf("err = %v", err)
	}
}
