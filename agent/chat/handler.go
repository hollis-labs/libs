package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/libs/ui/ui-go/ssekit"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
)

// Permission is what a caller is asked to be allowed to do.
type Permission string

// The permissions.
const (
	// PermRead: see the conversation, attach as a viewer, read events.
	PermRead Permission = "read"
	// PermControl: attach as a controller, hold the lease, submit, cancel,
	// answer approvals, reset.
	PermControl Permission = "control"
	// PermTakeover: take the lease from its holder.
	PermTakeover Permission = "takeover"
	// PermAdmin is reserved for the host; no route of this handler asks for it.
	PermAdmin Permission = "admin"
)

// Authorize checks the caller, whom the host's server has authenticated and
// put in ctx, for a permission on conversations owned by owner. A nil error
// allows. IDs in a request are never proof of anything.
type Authorize func(ctx context.Context, owner string, p Permission) error

// HandlerOption configures [NewHandler].
type HandlerOption func(*handler)

// WithHeartbeat sets how long an event stream may be quiet before a keepalive
// comment is written (default 15s; zero or less turns it off).
func WithHeartbeat(d time.Duration) HandlerOption {
	return func(h *handler) { h.heartbeat = d }
}

// WithMaxBodyBytes bounds a request body (default 1 MiB).
func WithMaxBodyBytes(n int64) HandlerOption {
	return func(h *handler) { h.maxBody = n }
}

type handler struct {
	svc       *Service
	authorize Authorize
	heartbeat time.Duration
	maxBody   int64
}

// NewHandler returns the wire's routes over svc. Mount it under any prefix
// with http.StripPrefix. The routes:
//
//	POST   /agents/{agent}/conversation
//	POST   /agents/{agent}/conversation/reset
//	GET    /conversations/{id}
//	POST   /conversations/{id}/attachments
//	DELETE /conversations/{id}/attachments/{attachment}
//	GET    /conversations/{id}/events?attachment=&after=
//	POST   /conversations/{id}/lease
//	POST   /conversations/{id}/lease/takeover
//	POST   /conversations/{id}/turns
//	POST   /conversations/{id}/runs/{run}/cancel
//	POST   /conversations/{id}/approvals/{approval}
//
// Errors are JSON: {"code", "message"} plus "generation" on stale_attachment
// and "conversation_id" (the successor) on conversation_closed.
func NewHandler(svc *Service, authorize Authorize, opts ...HandlerOption) (http.Handler, error) {
	if svc == nil || authorize == nil {
		return nil, errors.New("chat: NewHandler needs a Service and an Authorize")
	}
	h := &handler{svc: svc, authorize: authorize, heartbeat: 15 * time.Second, maxBody: 1 << 20}
	for _, o := range opts {
		o(h)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /agents/{agent}/conversation", h.ensure)
	mux.HandleFunc("POST /agents/{agent}/conversation/reset", h.reset)
	mux.HandleFunc("GET /conversations/{id}", h.get)
	mux.HandleFunc("POST /conversations/{id}/attachments", h.attach)
	mux.HandleFunc("DELETE /conversations/{id}/attachments/{attachment}", h.detach)
	mux.HandleFunc("GET /conversations/{id}/events", h.events)
	mux.HandleFunc("POST /conversations/{id}/lease", h.lease)
	mux.HandleFunc("POST /conversations/{id}/lease/takeover", h.takeover)
	mux.HandleFunc("POST /conversations/{id}/turns", h.turns)
	mux.HandleFunc("POST /conversations/{id}/runs/{run}/cancel", h.cancel)
	mux.HandleFunc("POST /conversations/{id}/approvals/{approval}", h.approval)
	return mux, nil
}

// errForbidden marks an Authorize refusal.
var errForbidden = errors.New("chat: forbidden")

type errorBody struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	Generation     uint64 `json:"generation,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the client going away is not ours to report
}

// writeError maps a Service error to the wire's status and code.
func (h *handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		closed   *ClosedError
		stale    *StaleError
		rejected *RejectedError
		bad      *InvalidError
		tooBig   *http.MaxBytesError
	)
	body := errorBody{Message: err.Error()}
	status := http.StatusInternalServerError
	switch {
	case errors.As(err, &closed):
		status, body.Code, body.ConversationID = http.StatusGone, "conversation_closed", closed.Successor
		body.Message = "the conversation was reset"
	case errors.As(err, &stale):
		status, body.Code, body.Generation = http.StatusConflict, "stale_attachment", stale.Generation
		body.Message = "the attachment is from an older generation; attach again"
	case errors.As(err, &rejected):
		status, body.Code, body.Message = http.StatusUnprocessableEntity, rejected.Code, rejected.Message
	case errors.As(err, &bad):
		status, body.Code, body.Message = http.StatusBadRequest, "invalid_request", bad.Reason
	case errors.As(err, &tooBig):
		status, body.Code, body.Message = http.StatusRequestEntityTooLarge, "body_too_large", "the request body is too large"
	case errors.Is(err, errForbidden):
		status, body.Code, body.Message = http.StatusForbidden, "forbidden", "not allowed"
	case errors.Is(err, ErrNotFound):
		status, body.Code, body.Message = http.StatusNotFound, "not_found", "no such conversation or agent"
	case errors.Is(err, ErrUnknownAttachment):
		status, body.Code, body.Message = http.StatusNotFound, "unknown_attachment", "no such attachment"
	case errors.Is(err, ErrUnknownApproval):
		status, body.Code, body.Message = http.StatusNotFound, "unknown_approval", "no such pending approval"
	case errors.Is(err, ErrLeaseRequired):
		status, body.Code, body.Message = http.StatusForbidden, "lease_required", "the attachment does not hold the input lease"
	case errors.Is(err, ErrNotController):
		status, body.Code, body.Message = http.StatusForbidden, "not_controller", "only a controller attachment can hold the lease"
	case errors.Is(err, ErrLeaseHeld):
		status, body.Code, body.Message = http.StatusConflict, "lease_held", "another attachment holds the lease"
	case errors.Is(err, ErrBusy):
		status, body.Code, body.Message = http.StatusConflict, "run_active", "a run is active"
	case errors.Is(err, ErrRunNotActive):
		status, body.Code, body.Message = http.StatusConflict, "run_not_active", "that run is not the active run"
	case errors.Is(err, ErrApprovalResolved):
		status, body.Code, body.Message = http.StatusConflict, "approval_resolved", "the approval was already answered or has expired"
	case errors.Is(err, ErrRequestConflict):
		status, body.Code, body.Message = http.StatusConflict, "request_conflict", "request_id was used for a different turn"
	case errors.Is(err, ErrScopeUnavailable):
		status, body.Code, body.Message = http.StatusUnprocessableEntity, "scope_unavailable", "that approval scope is not available here"
	case errors.Is(err, ErrRunner):
		status, body.Code, body.Message = http.StatusBadGateway, "runner_error", "the runner did not accept the request"
	case errors.Is(err, ErrServiceClosed):
		status, body.Code, body.Message = http.StatusServiceUnavailable, "unavailable", "the service is shutting down"
	default:
		body.Code, body.Message = "internal", "internal error"
	}
	if status >= http.StatusInternalServerError {
		// The request body is never logged: a turn's context can be private.
		h.svc.cfg.Logger.Error("chat: request failed", "method", r.Method, "path", r.URL.Path, "status", status, "error", err)
	}
	writeJSON(w, status, body)
}

// allow authorizes the caller for p on a conversation and returns nothing the
// caller has not been cleared to see.
func (h *handler) allow(r *http.Request, conversationID string, p Permission) error {
	rec, err := h.svc.Record(r.Context(), conversationID)
	if err != nil {
		return err
	}
	return h.allowOwner(r, rec.Owner, p)
}

func (h *handler) allowOwner(r *http.Request, owner string, p Permission) error {
	if err := h.authorize(r.Context(), owner, p); err != nil {
		return fmt.Errorf("%w: %w", errForbidden, err)
	}
	return nil
}

// decode reads a JSON body into v. An empty body is an empty object.
func (h *handler) decode(w http.ResponseWriter, r *http.Request, v any) error {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return err
		}
		return invalid("unreadable body")
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return invalid("body is not valid JSON for this route")
	}
	return nil
}

func (h *handler) ensure(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.Agent(r.Context(), r.PathValue("agent"))
	if err == nil {
		err = h.allowOwner(r, a.Owner, PermRead)
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	v, err := h.svc.EnsureConversation(r.Context(), r.PathValue("agent"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type attachmentBody struct {
	Attachment string `json:"attachment"`
}

func (h *handler) reset(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.Agent(r.Context(), r.PathValue("agent"))
	if err == nil {
		err = h.allowOwner(r, a.Owner, PermControl)
	}
	var body attachmentBody
	if err == nil {
		err = h.decode(w, r, &body)
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	v, err := h.svc.Reset(r.Context(), r.PathValue("agent"), body.Attachment)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.allow(r, id, PermRead); err != nil {
		h.writeError(w, r, err)
		return
	}
	v, err := h.svc.Conversation(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) attach(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Role   Role       `json:"role"`
		Stream StreamMode `json:"stream"`
	}
	if err := h.decode(w, r, &body); err != nil {
		h.writeError(w, r, err)
		return
	}
	perm := PermRead
	if body.Role == RoleController {
		perm = PermControl
	}
	if err := h.allow(r, id, perm); err != nil {
		h.writeError(w, r, err)
		return
	}
	att, head, err := h.svc.Attach(r.Context(), id, body.Role, body.Stream)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"attachment_id": att.ID, "head": head, "role": att.Role, "stream": att.Stream, "generation": att.Generation,
	})
}

func (h *handler) detach(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Attachment IDs are on the stream for every reader to see, so removing a
	// controller's attachment (and with it the lease) takes control.
	perm := PermRead
	if role, ok := h.svc.attachmentRole(r.Context(), id, r.PathValue("attachment")); ok && role == RoleController {
		perm = PermControl
	}
	err := h.allow(r, id, perm)
	if err == nil {
		err = h.svc.Detach(r.Context(), id, r.PathValue("attachment"))
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) lease(w http.ResponseWriter, r *http.Request) {
	h.leaseOp(w, r, PermControl, h.svc.AcquireLease)
}

func (h *handler) takeover(w http.ResponseWriter, r *http.Request) {
	h.leaseOp(w, r, PermTakeover, h.svc.TakeoverLease)
}

func (h *handler) leaseOp(w http.ResponseWriter, r *http.Request, p Permission, op func(context.Context, string, string) (Lease, error)) {
	id := r.PathValue("id")
	var body attachmentBody
	err := h.allow(r, id, p)
	if err == nil {
		err = h.decode(w, r, &body)
	}
	if err == nil && body.Attachment == "" {
		err = invalid("attachment is empty")
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	l, err := op(r.Context(), id, body.Attachment)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, LeaseView{Attachment: l.Holder, ExpiresAt: l.ExpiresAt})
}

func (h *handler) turns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Attachment string          `json:"attachment"`
		Text       string          `json:"text"`
		Context    json.RawMessage `json:"context"`
		Meta       json.RawMessage `json:"meta"`
		RequestID  string          `json:"request_id"`
	}
	err := h.allow(r, id, PermControl)
	if err == nil {
		err = h.decode(w, r, &body)
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	receipt, err := h.svc.SubmitAs(r.Context(), id, Submission{
		Attachment: body.Attachment, Text: body.Text, Context: body.Context, Meta: body.Meta, RequestID: body.RequestID,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, receipt)
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body attachmentBody
	err := h.allow(r, id, PermControl)
	if err == nil {
		err = h.decode(w, r, &body)
	}
	if err == nil && body.Attachment == "" {
		// The route has no required body in the table; the attachment may
		// travel in the query instead.
		if body.Attachment = r.URL.Query().Get("attachment"); body.Attachment == "" {
			err = invalid("attachment is empty")
		}
	}
	if err == nil {
		err = h.svc.Cancel(r.Context(), id, r.PathValue("run"), body.Attachment)
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": r.PathValue("run")})
}

func (h *handler) approval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Attachment string `json:"attachment"`
		Decision   string `json:"decision"`
		Scope      Scope  `json:"scope"`
	}
	err := h.allow(r, id, PermControl)
	if err == nil {
		err = h.decode(w, r, &body)
	}
	if err == nil {
		switch {
		case body.Attachment == "":
			err = invalid("attachment is empty")
		case body.Decision != "allow" && body.Decision != "deny":
			err = invalid(`decision must be "allow" or "deny"`)
		}
	}
	if err == nil {
		err = h.svc.AnswerApproval(r.Context(), id, r.PathValue("approval"), body.Attachment,
			Decision{Allow: body.Decision == "allow", Scope: body.Scope})
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// startCursor reads where the client starts: Last-Event-ID wins on a
// reconnect, otherwise "after" must say 0, a cursor or "head".
func startCursor(r *http.Request) (Cursor, error) {
	if last, ok := ssekit.ResumeCursor(r); ok {
		n, err := strconv.ParseUint(last, 10, 64)
		if err != nil {
			return Cursor{}, invalid("Last-Event-ID is not a cursor")
		}
		return After(n), nil
	}
	after := strings.TrimSpace(r.URL.Query().Get("after"))
	switch after {
	case "":
		return Cursor{}, invalid(`after is required: 0, a cursor or "head"`)
	case "head":
		return FromHead(), nil
	}
	n, err := strconv.ParseUint(after, 10, 64)
	if err != nil {
		return Cursor{}, invalid(`after must be 0, a cursor or "head"`)
	}
	return After(n), nil
}

func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	from, err := startCursor(r)
	if err == nil {
		err = h.allow(r, id, PermRead)
	}
	attachment := r.URL.Query().Get("attachment")
	if err == nil && attachment == "" {
		err = invalid("attachment is required")
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// Refusals (410, 409 stale_attachment, 404) are answered here, before the
	// response becomes a stream.
	st, err := h.svc.Events(r.Context(), id, attachment, from)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	defer st.Close()
	sw, err := ssekit.NewWriter(w)
	if err != nil {
		h.writeError(w, r, fmt.Errorf("chat: event stream: %w", err))
		return
	}
	src := ssekit.SourceFunc(func(ctx context.Context) (ssekit.Event, error) {
		ev, nerr := st.Next(ctx)
		if nerr != nil {
			return ssekit.Event{}, nerr
		}
		data, nerr := json.Marshal(ev)
		if nerr != nil {
			return ssekit.Event{}, fmt.Errorf("chat: encode event: %w", nerr)
		}
		out := ssekit.Event{Name: string(ev.Verb), Data: data}
		if ev.Seq != 0 {
			out.ID = strconv.FormatUint(ev.Seq, 10) // a gap has no cursor of its own
		}
		return out, nil
	})
	err = ssekit.Serve(r.Context(), sw, src, ssekit.WithHeartbeat(h.heartbeat))
	var slow *streamhub.SlowConsumerError
	switch {
	case errors.As(err, &slow):
		// The client fell behind and is closed; it resumes with the id of the
		// last event it was sent, which is this cursor.
		_ = sw.Comment("slow consumer; resume after " + strconv.FormatUint(uint64(slow.LastDelivered), 10))
	case err != nil && r.Context().Err() == nil:
		h.svc.cfg.Logger.Warn("chat: event stream ended", "conversation", id, "error", err)
	}
}
