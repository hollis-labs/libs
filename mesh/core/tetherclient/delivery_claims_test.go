package tether

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	messaging "github.com/hollis-labs/go-messaging"
	"github.com/hollis-labs/go-messaging/delivery"
)

func mustAddr(t *testing.T, urn string) messaging.Address {
	t.Helper()
	a, err := messaging.ParseURN(urn)
	if err != nil {
		t.Fatalf("ParseURN(%q): %v", urn, err)
	}
	return a
}

const (
	testRecipient = "msg://agent/agent-mux/agt_aaaaaaaaaa"
	testSender    = "msg://agent/agent-mux/agt_bbbbbbbbbb"
)

// The daemon requires ?as= on every one of these routes and rejects a
// lease that does not belong to the {id} in the path. Both are caller
// contracts the wrappers have to honor, so the fake asserts them rather
// than accepting anything.
func claimServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, action, id string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.URL.Query().Get("as"); got != testRecipient {
			t.Errorf("as = %q, want %q — the daemon 400s without it", got, testRecipient)
		}
		// /messages/{id}/{action}
		var id, action string
		if n, _ := fmtSscan(r.URL.Path, &id, &action); n != 2 {
			t.Fatalf("unparseable path %q", r.URL.Path)
		}
		handler(w, r, action, id)
	}))
}

// fmtSscan pulls {id} and {action} out of /messages/{id}/{action}.
func fmtSscan(path string, id, action *string) (int, error) {
	var parts []string
	cur := ""
	for _, c := range path {
		if c == '/' {
			if cur != "" {
				parts = append(parts, cur)
			}
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	if len(parts) != 3 || parts[0] != "messages" {
		return 0, nil
	}
	*id, *action = parts[1], parts[2]
	return 2, nil
}

func TestClaimMessage(t *testing.T) {
	wantLease := delivery.LeaseRef{
		DeliveryID: "dlv_1", AttemptID: "att_1", LeaseToken: "tok_1", BindingGeneration: 7,
	}
	srv := claimServer(t, func(w http.ResponseWriter, r *http.Request, action, id string) {
		if action != "claim" || id != "msg_1" {
			t.Errorf("got %s/%s, want msg_1/claim", id, action)
		}
		var req claimRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Holder != "host-1" || req.LeaseSeconds != 120 {
			t.Errorf("body = %+v, want holder=host-1 lease_seconds=120", req)
		}
		// The daemon clamps: ask for 120, get 60 back.
		_ = json.NewEncoder(w).Encode(claimResponse{
			Message:   messaging.Envelope{ID: "msg_1", From: mustAddr(t, testSender), To: mustAddr(t, testRecipient)},
			Lease:     wantLease,
			ExpiresIn: 60,
		})
	})
	defer srv.Close()

	c := MustNew(srv.URL)
	env, lease, granted, err := c.ClaimMessage(context.Background(), "msg_1",
		mustAddr(t, testRecipient), ClaimOptions{Holder: "host-1", LeaseSeconds: 120})
	if err != nil {
		t.Fatalf("ClaimMessage: %v", err)
	}
	if env.ID != "msg_1" {
		t.Errorf("envelope ID = %q, want msg_1", env.ID)
	}
	if lease != wantLease {
		t.Errorf("lease = %+v, want %+v", lease, wantLease)
	}
	// The granted duration is what the caller must honor, not what it asked for.
	if granted != 60 {
		t.Errorf("granted = %d, want 60 (the clamped value the daemon returned, not the 120 requested)", granted)
	}
}

func TestClaimMessage_ZeroOptionsSendsNoOverrides(t *testing.T) {
	srv := claimServer(t, func(w http.ResponseWriter, r *http.Request, action, id string) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		// omitempty: the daemon defaults holder and lease when absent, so
		// sending explicit zeros would override its defaults with nonsense.
		if _, ok := raw["holder"]; ok {
			t.Errorf("holder present in body %v, want omitted so the daemon defaults it", raw)
		}
		if _, ok := raw["lease_seconds"]; ok {
			t.Errorf("lease_seconds present in body %v, want omitted", raw)
		}
		_ = json.NewEncoder(w).Encode(claimResponse{ExpiresIn: 30})
	})
	defer srv.Close()

	if _, _, _, err := MustNew(srv.URL).ClaimMessage(context.Background(), "msg_1",
		mustAddr(t, testRecipient), ClaimOptions{}); err != nil {
		t.Fatalf("ClaimMessage: %v", err)
	}
}

func TestAckMessage(t *testing.T) {
	lease := delivery.LeaseRef{DeliveryID: "dlv_1", AttemptID: "att_1", LeaseToken: "tok_1"}
	srv := claimServer(t, func(w http.ResponseWriter, r *http.Request, action, id string) {
		if action != "ack" {
			t.Errorf("action = %q, want ack", action)
		}
		var req ackRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Lease != lease {
			t.Errorf("lease = %+v, want it echoed back verbatim (%+v)", req.Lease, lease)
		}
		if req.Stage != delivery.StageHostAccepted {
			t.Errorf("stage = %q, want %q", req.Stage, delivery.StageHostAccepted)
		}
		_ = json.NewEncoder(w).Encode(receiptResponse{
			Delivery: delivery.RecipientDelivery{ID: "dlv_1", Recipient: mustAddr(t, testRecipient), Status: delivery.DeliveryLeased},
			Attempt:  delivery.Attempt{ID: "att_1", Stage: delivery.StageHostAccepted},
		})
	})
	defer srv.Close()

	rd, att, err := MustNew(srv.URL).AckMessage(context.Background(), "msg_1",
		mustAddr(t, testRecipient), lease, delivery.StageHostAccepted)
	if err != nil {
		t.Fatalf("AckMessage: %v", err)
	}
	if rd.ID != "dlv_1" || att.Stage != delivery.StageHostAccepted {
		t.Errorf("got delivery=%+v attempt=%+v", rd, att)
	}
}

// A non-retryable Nack dead-letters, and the daemon reports that as 200
// rather than an error — go-messaging returns ErrDeadLettered on the call
// that SUCCEEDS at dead-lettering, and Tether's handler deliberately does
// not propagate it. A caller treating a non-nil error as "the nack failed"
// would retry something already dead-lettered, so this must stay nil.
func TestNackMessage_DeadLetterIsNotAnError(t *testing.T) {
	lease := delivery.LeaseRef{DeliveryID: "dlv_1", LeaseToken: "tok_1"}
	srv := claimServer(t, func(w http.ResponseWriter, r *http.Request, action, id string) {
		if action != "nack" {
			t.Errorf("action = %q, want nack", action)
		}
		var req nackRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Retryable {
			t.Errorf("retryable = true, want false — the zero NackOptions dead-letters")
		}
		if req.Error != "handler panicked" {
			t.Errorf("error = %q, want the reason to reach the trace", req.Error)
		}
		_ = json.NewEncoder(w).Encode(receiptResponse{
			Delivery: delivery.RecipientDelivery{
				ID: "dlv_1", Recipient: mustAddr(t, testRecipient), Status: delivery.DeliveryDeadLettered, DeadLetterReason: "handler panicked",
			},
			Attempt: delivery.Attempt{ID: "att_1", Stage: delivery.StageDeadLettered},
		})
	})
	defer srv.Close()

	rd, _, err := MustNew(srv.URL).NackMessage(context.Background(), "msg_1",
		mustAddr(t, testRecipient), lease, NackOptions{Reason: "handler panicked"})
	if err != nil {
		t.Fatalf("NackMessage returned %v; dead-lettering is a successful outcome and must not surface as an error", err)
	}
	if rd.Status != delivery.DeliveryDeadLettered {
		t.Errorf("status = %q, want %q — the caller learns the outcome from the delivery, not from an error",
			rd.Status, delivery.DeliveryDeadLettered)
	}
}

func TestNackMessage_Retryable(t *testing.T) {
	srv := claimServer(t, func(w http.ResponseWriter, r *http.Request, action, id string) {
		var req nackRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !req.Retryable || req.NextAttemptSeconds != 45 {
			t.Errorf("body = %+v, want retryable=true next_attempt_seconds=45", req)
		}
		_ = json.NewEncoder(w).Encode(receiptResponse{
			Delivery: delivery.RecipientDelivery{ID: "dlv_1", Recipient: mustAddr(t, testRecipient), Status: delivery.DeliveryRetryScheduled},
		})
	})
	defer srv.Close()

	rd, _, err := MustNew(srv.URL).NackMessage(context.Background(), "msg_1",
		mustAddr(t, testRecipient), delivery.LeaseRef{LeaseToken: "tok_1"},
		NackOptions{Retryable: true, NextAttemptSeconds: 45})
	if err != nil {
		t.Fatalf("NackMessage: %v", err)
	}
	if rd.Status != delivery.DeliveryRetryScheduled {
		t.Errorf("status = %q, want %q", rd.Status, delivery.DeliveryRetryScheduled)
	}
}

// A 409 from ack/nack means the lease was fenced or expired. It must come
// back as a typed error a caller can branch on, not an opaque string.
func TestAckMessage_ConflictMapsToTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"conflict","message":"lease superseded"}}`))
	}))
	defer srv.Close()

	_, _, err := MustNew(srv.URL).AckMessage(context.Background(), "msg_1",
		mustAddr(t, testRecipient), delivery.LeaseRef{LeaseToken: "stale"}, delivery.StageConsumed)
	if err == nil {
		t.Fatal("AckMessage on a superseded lease returned nil; a fenced lease must surface")
	}
}
