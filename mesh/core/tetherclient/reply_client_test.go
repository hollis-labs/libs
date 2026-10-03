package tether_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	tether "github.com/hollis-labs/substrate/mesh/tetherclient"
)

func TestReplyWire(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupt), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.EscapedPath() != "/messages/parent%2Fwith%20space/reply" || r.URL.Query().Get("as") != "msg://user/local/test" {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Idempotency-Key") != "same-key" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("headers = %v", r.Header)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["body"] != "use the second option" || body["idempotency_key"] != nil {
					t.Errorf("body = %v", body)
				}
				if v, _ := body["interrupt"].(bool); v != interrupt {
					t.Errorf("interrupt = %v", body)
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = fmt.Fprintf(w, `{"reply_id":"reply1","parent_id":"parent/with space","state":"queued","target_session_id":"session1","interrupt":"interrupt_timeout","duplicate":%t}`, calls > 1)
			}))
			defer srv.Close()
			c := tether.MustNew(srv.URL, tether.WithSelfURN("msg://user/local/test"), tether.WithToken("test-token"))
			for i := 0; i < 2; i++ {
				out, err := c.Reply(context.Background(), "parent/with space", "use the second option", tether.ReplyOptions{Interrupt: interrupt, IdempotencyKey: "same-key"})
				if err != nil || out.ReplyID != "reply1" || out.ParentID != "parent/with space" || out.TargetSessionID != "session1" || out.State != "queued" || out.Interrupt != "interrupt_timeout" || out.Duplicate != (i > 0) {
					t.Fatalf("receipt = %+v, %v", out, err)
				}
			}
			if calls != 2 {
				t.Fatalf("calls = %d", calls)
			}
		})
	}
}

func TestReplyTypedErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{
		{409, tether.CodeInterruptUnsupported}, {409, tether.CodeTurnNotYetStarted}, {409, tether.CodeTurnFeedUnavailable}, {409, tether.CodeIdempotencyConflict},
		{413, "payload_too_large"}, {400, "invalid_request"}, {400, tether.CodeReplyTargetNotSession},
	} {
		t.Run(tc.code, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"error":{"code":%q,"message":"refused"}}`, tc.code)
			}))
			defer srv.Close()
			_, err := tether.MustNew(srv.URL, tether.WithToken("")).Reply(context.Background(), "parent", "text", tether.ReplyOptions{Interrupt: true})
			var apiErr *tether.APIError
			if !errors.As(err, &apiErr) || apiErr.Message != "refused" || !errors.Is(err, &tether.APIError{StatusCode: tc.status, Code: tc.code}) || calls != 1 {
				t.Fatalf("error = %v; calls = %d", err, calls)
			}
		})
	}
}

func TestReplyCancelledAndEmptyID(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(202) }))
	defer srv.Close()
	c := tether.MustNew(srv.URL, tether.WithToken(""))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Reply(ctx, "parent", "body", tether.ReplyOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if _, err := c.Reply(context.Background(), "", "body", tether.ReplyOptions{}); err == nil {
		t.Fatal("empty ID accepted")
	}
	if calls != 0 {
		t.Fatalf("calls = %d", calls)
	}
}
