package agenthost

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hollis-labs/libs/agent/core/transport/httpstream"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub"
)

// Handler serves every route of the wire under prefix (default "/api/agent"). owner says
// whose request it is, by the application's own authentication; an error answers 401.
// Requests must reach it with their full path: mount it at both prefix and prefix+"/".
func (h *Host) Handler(prefix string, owner func(*http.Request) (string, error)) http.Handler {
	if prefix == "" {
		prefix = "/api/agent"
	}
	prefix = "/" + strings.Trim(prefix, "/")
	s := &server{h: h, owner: owner}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+prefix, s.state)
	mux.HandleFunc("GET "+prefix+"/thread", s.thread)
	mux.HandleFunc("DELETE "+prefix+"/thread", s.newThread)
	mux.HandleFunc("POST "+prefix+"/turns", s.turn)
	mux.HandleFunc("GET "+prefix+"/runs/{id}/events", s.events)
	mux.HandleFunc("POST "+prefix+"/runs/{id}/tool-results", s.toolResult)
	mux.HandleFunc("POST "+prefix+"/runs/{id}/tool-alive", s.toolAlive)
	mux.HandleFunc("POST "+prefix+"/runs/{id}/approvals", s.approval)
	mux.HandleFunc("POST "+prefix+"/runs/{id}/cancel", s.cancel)
	return mux
}

type server struct {
	h     *Host
	owner func(*http.Request) (string, error)
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail writes a flat {code, message} error body.
func fail(w http.ResponseWriter, status int, message string) {
	code := map[int]string{400: "invalid_request", 401: "unauthenticated", 404: "not_found", 409: "conflict",
		410: "gone", 413: "too_large", 500: "internal", 503: "unavailable"}[status]
	reply(w, status, map[string]string{"code": code, "message": message})
}

// decode reads a JSON body of at most limit bytes.
func decode(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, 413, "The request is too large.")
		} else {
			fail(w, 400, "The request body is not valid JSON.")
		}
		return false
	}
	return true
}

func (s *server) who(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, err := s.owner(r)
	if err != nil || id == "" {
		fail(w, 401, "Please sign in.")
		return "", false
	}
	return id, true
}

func (s *server) state(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.who(w, r); !ok {
		return
	}
	cfg := s.h.Config()
	reply(w, 200, map[string]any{"available": true, "ready": cfg.Ready(), "model": cfg.Model})
}

func (s *server) thread(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.who(w, r)
	if !ok {
		return
	}
	thread, err := s.h.store.MainThread(r.Context(), owner)
	if err != nil {
		fail(w, 500, "The conversation could not be read.")
		return
	}
	msgs, err := s.h.store.Messages(r.Context(), thread)
	if err != nil {
		fail(w, 500, "The conversation could not be read.")
		return
	}
	reply(w, 200, map[string]any{"thread_id": thread, "messages": msgs, "active_run": s.h.ActiveRun(thread)})
}

func (s *server) newThread(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.who(w, r)
	if !ok {
		return
	}
	if thread, err := s.h.store.MainThread(r.Context(), owner); err == nil && s.h.ActiveRun(thread) != "" {
		fail(w, 409, "The agent is still working. Stop it first.")
		return
	}
	if err := s.h.store.ArchiveMain(r.Context(), owner); err != nil {
		fail(w, 500, "The conversation could not be put away.")
		return
	}
	w.WriteHeader(204)
}

func (s *server) turn(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.who(w, r)
	if !ok {
		return
	}
	var in struct {
		Text     string          `json:"text"`
		ClientID string          `json:"client_id"`
		Context  json.RawMessage `json:"context"`
		Tools    []ToolSpec      `json:"tools"`
		Auto     string          `json:"auto"`
	}
	if !decode(w, r, &in, 512<<10) {
		return
	}
	id, err := s.h.Start(r.Context(), Turn{Owner: owner, Text: in.Text, ClientID: in.ClientID, Context: in.Context, Tools: in.Tools, Auto: in.Auto})
	switch {
	case errors.Is(err, ErrNotReady):
		fail(w, 503, "No model is configured for the agent.")
	case errors.Is(err, ErrBusy):
		fail(w, 409, "The agent is still working on the last message.")
	case err != nil:
		fail(w, 400, err.Error())
	default:
		reply(w, 200, map[string]string{"run_id": id})
	}
}

// run checks that the run in the path is the caller's.
func (s *server) run(w http.ResponseWriter, r *http.Request) (owner, id string, ok bool) {
	if owner, ok = s.who(w, r); !ok {
		return "", "", false
	}
	id = r.PathValue("id")
	if o, err := s.h.store.RunOwner(r.Context(), id); err != nil || o != owner {
		fail(w, 404, "No such run.")
		return "", "", false
	}
	return owner, id, true
}

// events streams a run as chatstream/v1 server-sent events. A reader that was cut off
// resumes with Last-Event-ID; a finished run can be replayed for a few minutes.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.run(w, r)
	if !ok {
		return
	}
	after, err := httpstream.ParseCursor(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	sub, err := s.h.hub.Subscribe(r.Context(), id, streamhub.SubscribeOptions{After: streamhub.Seq(after)})
	if err != nil {
		// The run ended long enough ago that its events are gone: the thread has the result.
		fail(w, 410, "That run's events are no longer kept.")
		return
	}
	_ = httpstream.Write(w, r, sub, httpstream.State{RunID: id, After: after}, false)
}

func (s *server) cancel(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := s.run(w, r)
	if !ok {
		return
	}
	// A run that already ended has nothing to stop: that is not an error.
	_ = s.h.Cancel(owner, id)
	w.WriteHeader(204)
}

func (s *server) toolResult(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := s.run(w, r)
	if !ok {
		return
	}
	var in struct {
		CallID  string  `json:"call_id"`
		Content string  `json:"content"`
		IsError bool    `json:"is_error"`
		Images  []Image `json:"images"`
	}
	if !decode(w, r, &in, 6<<20) {
		return
	}
	err := s.h.ToolResult(owner, id, in.CallID, in.Content, in.IsError, in.Images...)
	switch {
	case errors.Is(err, ErrNoRun), errors.Is(err, ErrNotWaiting):
		fail(w, 409, "The run is no longer waiting for that call.")
	case err != nil:
		fail(w, 400, err.Error())
	default:
		w.WriteHeader(204)
	}
}

// toolAlive is the page saying it is still running a call. 409 tells it the run has
// stopped waiting, so it must not go on to change anything.
func (s *server) toolAlive(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := s.run(w, r)
	if !ok {
		return
	}
	var in struct {
		CallID string `json:"call_id"`
	}
	if !decode(w, r, &in, 16<<10) {
		return
	}
	if err := s.h.ToolAlive(owner, id, in.CallID); err != nil {
		fail(w, 409, "The run is no longer waiting for that call.")
		return
	}
	w.WriteHeader(204)
}

func (s *server) approval(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := s.run(w, r)
	if !ok {
		return
	}
	var in struct {
		ApprovalID string `json:"approval_id"`
		Decision   string `json:"decision"`
	}
	if !decode(w, r, &in, 16<<10) {
		return
	}
	err := s.h.Approve(owner, id, in.ApprovalID, in.Decision)
	switch {
	case errors.Is(err, ErrNoRun), errors.Is(err, ErrNotWaiting):
		fail(w, 409, "That question is no longer waiting.")
	case err != nil:
		fail(w, 400, err.Error())
	default:
		w.WriteHeader(204)
	}
}
