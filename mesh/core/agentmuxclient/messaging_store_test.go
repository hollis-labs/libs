package agentmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentmux "github.com/hollis-labs/go-agentmux-client"
	messaging "github.com/hollis-labs/go-messaging"
	"github.com/hollis-labs/go-messaging/memstore"
	"github.com/hollis-labs/go-messaging/messagingtest"
)

// TestHTTPStore_Contract runs all 13 messaging.Store contract sub-tests against
// httpStore connected to an in-process httptest.Server backed by memstore.
func TestHTTPStore_Contract(t *testing.T) {
	messagingtest.RunContract(t, func(t *testing.T) messaging.Store {
		ms := memstore.New()
		disp := messaging.NewDispatcher(ms)
		srv := httptest.NewServer(newMsgTestHandler(ms, disp))
		t.Cleanup(srv.Close)

		client, err := agentmux.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		return client.AsStore()
	})
}

// msgTestHandler serves the /messages/* routes backed by an in-process memstore.
type msgTestHandler struct {
	store *memstore.Store
	disp  messaging.Dispatcher
}

func newMsgTestHandler(ms *memstore.Store, disp messaging.Dispatcher) http.Handler {
	h := &msgTestHandler{store: ms, disp: disp}
	mux := http.NewServeMux()

	// More-specific patterns must be registered first so they take priority.
	mux.HandleFunc("POST /messages/request", h.handleRequest)
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
