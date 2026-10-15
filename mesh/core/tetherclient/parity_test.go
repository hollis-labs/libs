package tether

// Tether API parity, 2026-10-01: notify, turn_failed, the killed state and
// the 404 for an unknown launch.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	messaging "github.com/hollis-labs/substrate/mesh/messaging"
)

func TestNotifySendsWireKeysAndDecodesTheResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/messages/notify" {
			t.Fatalf("got %s %s", r.Method, r.URL.Path)
		}
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for key, want := range map[string]any{
			"from":      "msg://user/local/operator",
			"to":        "msg://agent/local/worker",
			"kind":      "request",
			"urgency":   UrgencyHigh,
			"wake":      false,
			"wake_text": "look",
		} {
			if raw[key] != want {
				t.Fatalf("%s = %v; want %v (body %v)", key, raw[key], want, raw)
			}
		}
		if _, ok := raw["session_id"]; ok {
			t.Fatalf("empty session_id sent: %v", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"message":{"id":"m1","kind":"request","from":"msg://user/local/operator","to":"msg://agent/local/worker"},
			"unread_count":2,"wake_attempted":false,"wake_delivered":false,"wake_reason":"session-not-running"}`))
	}))
	defer srv.Close()

	wake := false
	got, err := MustNew(srv.URL).Notify(context.Background(), NotifyRequest{
		Kind:     messaging.MsgKindRequest,
		From:     messaging.Address{Kind: messaging.KindUser, Authority: "local", ID: "operator"},
		To:       messaging.Address{Kind: messaging.KindAgent, Authority: "local", ID: "worker"},
		Urgency:  UrgencyHigh,
		Wake:     &wake,
		WakeText: "look",
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got.Message.ID != "m1" || got.UnreadCount != 2 || got.WakeAttempted || got.WakeReason != WakeReasonSessionNotRunning {
		t.Fatalf("result = %+v", got)
	}
}

func TestNotifyErrorIsAnAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStatusJSON(t, w, http.StatusBadRequest, ErrorResponse{Error: ErrorDetail{Code: ErrorCodeInvalidRequest, Message: "from and to are required"}})
	}))
	defer srv.Close()
	_, err := MustNew(srv.URL).Notify(context.Background(), NotifyRequest{})
	if !errors.Is(err, &APIError{StatusCode: http.StatusBadRequest, Code: ErrorCodeInvalidRequest}) {
		t.Fatalf("err = %v; want a 400 invalid_request APIError", err)
	}
}

// An unknown launch is a 404 not_found since Tether #67; before, it was 500.
func TestIsNotFoundForUnknownLaunch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStatusJSON(t, w, http.StatusNotFound, ErrorResponse{Error: ErrorDetail{Code: ErrorCodeNotFound, Message: `launch "nope" not found`}})
	}))
	defer srv.Close()
	_, err := MustNew(srv.URL).CreateSession(context.Background(), "nope")
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = false", err)
	}
	if IsNotFound(errors.New("other")) || IsNotFound(&APIError{StatusCode: http.StatusConflict, Code: ErrorCodeConflict}) || IsNotFound(nil) {
		t.Fatal("IsNotFound matched a non-404")
	}
}

// A failed subprocess turn is a 502 turn_failed since Tether #70, surfaced
// once and matchable by code.
func TestSendTurnTurnFailedIsMatchable(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeStatusJSON(t, w, http.StatusBadGateway, ErrorResponse{Error: ErrorDetail{Code: CodeTurnFailed, Message: "exit status 1"}})
	}))
	defer srv.Close()
	err := MustNew(srv.URL).SendTurn(context.Background(), "s1", "hi")
	if !errors.Is(err, &APIError{StatusCode: http.StatusBadGateway, Code: CodeTurnFailed}) {
		t.Fatalf("err = %v; want 502 turn_failed", err)
	}
	if calls != 1 {
		t.Fatalf("daemon called %d times; a failed turn must not be retried", calls)
	}
}

func TestIsTerminalSessionState(t *testing.T) {
	for state, want := range map[string]bool{
		SessionStateCreated:   false,
		SessionStateLaunching: false,
		SessionStateRunning:   false,
		SessionStateCompleted: true,
		SessionStateFailed:    true,
		SessionStateKilled:    true,
		"":                    false,
	} {
		if got := IsTerminalSessionState(state); got != want {
			t.Fatalf("IsTerminalSessionState(%q) = %v; want %v", state, got, want)
		}
	}
}

func TestParseSessionStateChange(t *testing.T) {
	got, err := ParseSessionStateChange(`{"from":"running","to":"killed","exit_code":0}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.From != SessionStateRunning || got.To != SessionStateKilled || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("got %+v", got)
	}
	if _, err := ParseSessionStateChange(`not json`); err == nil {
		t.Fatal("bad payload accepted")
	}
}
