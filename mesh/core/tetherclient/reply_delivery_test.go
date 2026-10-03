package tether_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tether "github.com/hollis-labs/substrate/mesh/tetherclient"
)

func TestReplyDelivery(t *testing.T) {
	for _, tc := range []struct {
		state  tether.ReplyState
		reason string
	}{
		{tether.ReplyPending, ""}, {tether.ReplyQueued, tether.ReplyReasonWaitingForIdle}, {tether.ReplyDelivering, ""},
		{tether.ReplyDelivered, tether.ReplyReasonHandedOff}, {tether.ReplyDelivered, tether.ReplyReasonTurnFailed},
		{tether.ReplyUndeliverable, tether.ReplyReasonNoTurnFeed}, {tether.ReplyUndeliverable, tether.ReplyReasonDaemonRestartedDuringDelivery},
		{tether.ReplyUndeliverable, tether.ReplyReasonInterruptUnconfirmed},
	} {
		t.Run(string(tc.state)+tc.reason, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.EscapedPath() != "/messages/reply%2Fwith%20space/delivery" || r.URL.Query().Get("as") != "msg://user/local/test" {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				_, _ = fmt.Fprintf(w, `{"reply_id":"r1","parent_id":"p1","state":%q,"reason":%q,"detail":"one line","original_session_id":"s1","target_session_id":"s2","delivered_to_session_id":"s2","interrupt_requested":true,"attempts":2,"next_attempt_at":"2026-10-02T14:00:00Z","created_at":"2026-10-02T14:00:00Z","updated_at":"2026-10-02T14:00:00Z","settled_at":"2026-10-02T14:00:00Z"}`, tc.state, tc.reason)
			}))
			defer srv.Close()
			c := tether.MustNew(srv.URL, tether.WithToken(""), tether.WithSelfURN("msg://user/local/test"))
			out, err := c.ReplyDelivery(context.Background(), "reply/with space")
			at := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
			if err != nil || out.ReplyID != "r1" || out.ParentID != "p1" || out.State != tc.state || out.Reason != tc.reason || out.Detail != "one line" || out.OriginalSessionID != "s1" || out.TargetSessionID != "s2" || out.DeliveredToSessionID != "s2" || !out.InterruptRequested || out.Attempts != 2 || !out.CreatedAt.Equal(at) || !out.UpdatedAt.Equal(at) || out.NextAttemptAt == nil || !out.NextAttemptAt.Equal(at) || out.SettledAt == nil || !out.SettledAt.Equal(at) {
				t.Fatalf("delivery = %+v, %v", out, err)
			}
		})
	}
}

func TestReplyDeliveryOptionalAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/messages/missing/delivery" {
			w.WriteHeader(404)
			_, _ = fmt.Fprint(w, `{"error":{"code":"not_found","message":"unknown reply"}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"reply_id":"r","state":"queued"}`)
	}))
	defer srv.Close()
	c := tether.MustNew(srv.URL, tether.WithToken(""))
	out, err := c.ReplyDelivery(context.Background(), "r")
	if err != nil || out.NextAttemptAt != nil || out.SettledAt != nil || out.Reason != "" {
		t.Fatalf("delivery = %+v, %v", out, err)
	}
	_, err = c.ReplyDelivery(context.Background(), "missing")
	if !errors.Is(err, &tether.APIError{StatusCode: 404, Code: "not_found"}) {
		t.Fatalf("error = %v", err)
	}
	if _, err = c.ReplyDelivery(context.Background(), ""); err == nil {
		t.Fatal("empty ID accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.ReplyDelivery(ctx, "r"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
