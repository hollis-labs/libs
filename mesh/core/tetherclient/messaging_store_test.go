package tether_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	messaging "github.com/hollis-labs/go-messaging"
	"github.com/hollis-labs/go-messaging/memstore"
	"github.com/hollis-labs/go-messaging/messagingtest"
	tether "github.com/hollis-labs/go-tether-client"
)

// TestHTTPStore_Contract runs all 13 messaging.Store contract sub-tests against
// httpStore connected to an in-process httptest.Server backed by memstore.
func TestHTTPStore_Contract(t *testing.T) {
	messagingtest.RunContract(t, func(t *testing.T) messaging.Store {
		ms := memstore.New()
		disp := messaging.NewDispatcher(ms)
		srv := httptest.NewServer(newMsgTestHandler(ms, disp))
		t.Cleanup(srv.Close)

		// WithSelfURN is required for Get/Thread (see messaging_store.go) --
		// this fake handler doesn't enforce Tether's real "?as= must be a
		// party to the message" ownership check (that's Tether's own
		// concern, covered by its own test suite), so any fixed identity
		// unblocks the generic cross-implementation conformance suite here.
		client, err := tether.New(srv.URL, tether.WithSelfURN("msg://agent/test/contract-caller"))
		if err != nil {
			t.Fatal(err)
		}
		return client.AsStore()
	})
}

// TestHTTPStore_GetAndThread_RequireSelfURN is the regression test for the
// gap this task's own review found: Get and Thread have no
// recipient/address parameter to derive Tether's required ?as= claim from,
// so a Client built without WithSelfURN must fail fast, client-side,
// instead of sending a request the daemon will reject.
func TestHTTPStore_GetAndThread_RequireSelfURN(t *testing.T) {
	var sawRequest bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client, err := tether.New(srv.URL) // no WithSelfURN
	if err != nil {
		t.Fatal(err)
	}
	store := client.AsStore()

	if _, err := store.Get(context.Background(), "some-id"); !errors.Is(err, tether.ErrSelfURNRequired) {
		t.Errorf("Get: got %v, want ErrSelfURNRequired", err)
	}
	if _, err := store.Thread(context.Background(), "some-thread", messaging.Filter{}); !errors.Is(err, tether.ErrSelfURNRequired) {
		t.Errorf("Thread: got %v, want ErrSelfURNRequired", err)
	}
	if sawRequest {
		t.Error("expected Get/Thread to fail client-side, without ever reaching the daemon")
	}
}

// TestHTTPStore_SendsAsQueryParam is the regression test proving each read
// call actually attaches Tether's required ?as= claim to the outgoing HTTP
// request: Get/Thread assert the client's configured self URN, Inbox/
// Subscribe assert the `to` address they were already called with.
func TestHTTPStore_SendsAsQueryParam(t *testing.T) {
	const self = "msg://agent/test/self"
	to := messaging.Address{Kind: messaging.KindAgent, Authority: "test", ID: "bob"}

	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/thread/"), strings.Contains(r.URL.Path, "/inbox"):
			json.NewEncoder(w).Encode(map[string]any{"messages": []messaging.Envelope{}}) //nolint:errcheck
		case r.URL.Path == "/messages/subscribe":
			flusher := w.(http.Flusher)
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
		default:
			json.NewEncoder(w).Encode(messaging.Envelope{ //nolint:errcheck
				ID:   "x",
				Kind: messaging.MsgKindNotice,
				From: messaging.Address{Kind: messaging.KindAgent, Authority: "test", ID: "alice"},
				To:   to,
			})
		}
	}))
	t.Cleanup(srv.Close)

	client, err := tether.New(srv.URL, tether.WithSelfURN(self))
	if err != nil {
		t.Fatal(err)
	}
	store := client.AsStore()
	ctx := context.Background()

	if _, err := store.Get(ctx, "some-id"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := gotQuery.Get("as"); got != self {
		t.Errorf("Get: ?as=%q, want %q", got, self)
	}

	if _, err := store.Thread(ctx, "some-thread", messaging.Filter{}); err != nil {
		t.Fatalf("Thread: %v", err)
	}
	if got := gotQuery.Get("as"); got != self {
		t.Errorf("Thread: ?as=%q, want %q", got, self)
	}

	if _, err := store.Inbox(ctx, to, messaging.Filter{}); err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if got := gotQuery.Get("as"); got != to.URN() {
		t.Errorf("Inbox: ?as=%q, want %q (matching `to`)", got, to.URN())
	}

	subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ch, err := store.Subscribe(subCtx, to, messaging.Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	cancel()
	for range ch {
	}
	if got := gotQuery.Get("as"); got != to.URN() {
		t.Errorf("Subscribe: ?as=%q, want %q (matching `to`)", got, to.URN())
	}
}

// msgTestHandler serves the /messages/* routes backed by an in-process memstore.
type msgTestHandler struct {
	store *memstore.Store
	disp  messaging.Dispatcher
}

func newMsgTestHandler(ms *memstore.Store, disp messaging.Dispatcher) http.Handler {
	h := &msgTestHandler{store: ms, disp: disp}
	mux := http.NewServeMux()

	// More-specific patterns registered first to take priority over wildcards.
	mux.HandleFunc("POST /messages/request", h.handleRequest)
	mux.HandleFunc("GET /messages/subscribe", h.handleSubscribe)
	mux.HandleFunc("GET /messages/inbox", h.handleInbox)
	mux.HandleFunc("GET /messages/thread/{threadID}", h.handleThread)
	mux.HandleFunc("GET /messages/{id}", h.handleGet)
	mux.HandleFunc("POST /messages", h.handleSend)
	mux.HandleFunc("POST /messages/{id}/consume", h.handleConsume)
	mux.HandleFunc("POST /messages/{id}/cancel", h.handleCancel)

	return mux
}

func (h *msgTestHandler) handleSend(w http.ResponseWriter, r *http.Request) {
	var env messaging.Envelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		writeTestErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	out, err := h.store.Send(r.Context(), env)
	if err != nil {
		if errors.Is(err, messaging.ErrPresetLifecycle) {
			writeTestErr(w, http.StatusUnprocessableEntity, "preset_lifecycle", err.Error())
			return
		}
		writeTestErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(out) //nolint:errcheck
}

func (h *msgTestHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	env, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, messaging.ErrNotFound) {
			writeTestErr(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeTestErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(env) //nolint:errcheck
}

func (h *msgTestHandler) handleInbox(w http.ResponseWriter, r *http.Request) {
	toURN := r.URL.Query().Get("to")
	to, err := messaging.ParseURN(toURN)
	if err != nil {
		writeTestErr(w, http.StatusBadRequest, "bad_request", "invalid to address")
		return
	}
	f := parseFilter(r)
	msgs, err := h.store.Inbox(r.Context(), to, f)
	if err != nil {
		writeTestErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if msgs == nil {
		msgs = []messaging.Envelope{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"messages": msgs}) //nolint:errcheck
}

func (h *msgTestHandler) handleThread(w http.ResponseWriter, r *http.Request) {
	threadID := r.PathValue("threadID")
	f := parseFilter(r)
	msgs, err := h.store.Thread(r.Context(), threadID, f)
	if err != nil {
		writeTestErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if msgs == nil {
		msgs = []messaging.Envelope{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"messages": msgs}) //nolint:errcheck
}

func (h *msgTestHandler) handleConsume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	asURN := r.URL.Query().Get("as")
	recipient, err := messaging.ParseURN(asURN)
	if err != nil {
		writeTestErr(w, http.StatusBadRequest, "bad_request", "invalid as address")
		return
	}
	if err := h.store.Consume(r.Context(), id, recipient); err != nil {
		if errors.Is(err, messaging.ErrNotFound) {
			writeTestErr(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeTestErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *msgTestHandler) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.Cancel(r.Context(), id); err != nil {
		if errors.Is(err, messaging.ErrNotFound) {
			writeTestErr(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeTestErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSubscribe streams newly-created envelopes for a recipient as SSE.
func (h *msgTestHandler) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeTestErr(w, http.StatusInternalServerError, "internal", "streaming not supported")
		return
	}
	toURN := r.URL.Query().Get("to")
	to, err := messaging.ParseURN(toURN)
	if err != nil {
		writeTestErr(w, http.StatusBadRequest, "bad_request", "invalid to address")
		return
	}
	f := parseFilter(r)

	ch, err := h.store.Subscribe(r.Context(), to, f)
	if err != nil {
		writeTestErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case env, open := <-ch:
			if !open {
				return
			}
			b, err := json.Marshal(env)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			flusher.Flush()
		case <-ping.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleRequest implements the blocking POST /messages/request endpoint.
// Uses the in-process memstore dispatcher for request/reply semantics.
func (h *msgTestHandler) handleRequest(w http.ResponseWriter, r *http.Request) {
	timeout := 30 * time.Second
	if s := r.URL.Query().Get("timeout"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	var env messaging.Envelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		writeTestErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	out, err := h.disp.Request(ctx, env)
	if err != nil {
		if errors.Is(err, messaging.ErrRequestTimeout) || errors.Is(err, context.DeadlineExceeded) {
			writeTestErr(w, http.StatusGatewayTimeout, "request_timeout", "request timed out")
			return
		}
		writeTestErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out) //nolint:errcheck
}

// parseFilter extracts a messaging.Filter from query params.
func parseFilter(r *http.Request) messaging.Filter {
	var f messaging.Filter
	if kinds := r.URL.Query().Get("kind"); kinds != "" {
		for _, k := range strings.Split(kinds, ",") {
			if k != "" {
				f.Kind = append(f.Kind, messaging.Kind(k))
			}
		}
	}
	if tid := r.URL.Query().Get("thread_id"); tid != "" {
		f.ThreadID = tid
	}
	return f
}

func writeTestErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"error": map[string]string{"code": code, "message": msg},
	})
}
