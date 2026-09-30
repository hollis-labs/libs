package federation

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	gomsg "github.com/hollis-labs/go-messaging"
)

// BasePath is the route prefix the surface is mounted at. It matches
// httpstore.TorqueFederationProfile, the dialect Dial speaks.
const BasePath = "/federation/v1/messages"

// ReservedMetadataPrefix is the Envelope.Metadata key namespace reserved for a
// future signed-envelope scheme. A server refuses an inbound envelope that uses
// it, so no peer can occupy the namespace before it exists.
const ReservedMetadataPrefix = "fed."

const (
	// DefaultMaxPayloadBytes caps an envelope payload (Torque's broker limit).
	DefaultMaxPayloadBytes = 256 * 1024
	// DefaultMaxListLimit caps how many envelopes one Thread or Inbox call
	// returns, and is used when the caller sends no limit.
	DefaultMaxListLimit = 1000

	maxConsumeBodyBytes = 16 << 10
	// maxEnvelopeOverheadBytes is the room a Send body has beyond twice the payload cap.
	maxEnvelopeOverheadBytes = 16 << 10
	writeTimeout             = 30 * time.Second
	streamWriteTimeout       = 10 * time.Second
	keepAliveInterval        = 15 * time.Second
)

// ServerOption configures a Server.
type ServerOption func(*Server)

// WithAuditor sets the function called with an AuditRecord for every request the
// server refuses or serves. The default discards them. It must return quickly
// and be safe for concurrent use.
func WithAuditor(f func(AuditRecord)) ServerOption { return func(s *Server) { s.audit = f } }

// WithMaxPayloadBytes sets the largest envelope payload accepted (default
// DefaultMaxPayloadBytes). n <= 0 keeps the default.
func WithMaxPayloadBytes(n int) ServerOption {
	return func(s *Server) {
		if n > 0 {
			s.maxPayload = n
		}
	}
}

// WithMaxListLimit sets the most envelopes one list call returns (default
// DefaultMaxListLimit). n <= 0 keeps the default.
func WithMaxListLimit(n int) ServerOption {
	return func(s *Server) {
		if n > 0 {
			s.maxList = n
		}
	}
}

// WithClock sets the server's clock, for the audit record's time. Tests only.
func WithClock(now func() time.Time) ServerOption { return func(s *Server) { s.now = now } }

// Server is the federation HTTP surface: the operations of its OpSet over the
// install's local Store, behind an IdentityResolver and the authorization layer.
//
// It wraps the local Store directly, never a Router: a request that reached this
// server was routed here because this install homes the governing authority, and
// the authorizer checks that again on arrival. Wrapping the Router would let a
// request be re-dispatched straight back out, the relay this design forbids.
type Server struct {
	store      gomsg.Store
	resolver   IdentityResolver
	ops        OpSet
	authz      *authorizer
	audit      func(AuditRecord)
	maxPayload int
	maxList    int
	now        func() time.Time
	handler    http.Handler
}

// NewServer builds the surface over local. resolver authenticates callers; ops
// says what is exposed (nil means DefaultOpSet, an OpSet that turns everything
// off is an error); localAuthorities are the authorities this install homes.
//
// Unlike a constructor that cannot fail, it returns an error for every
// misconfiguration (nil store or resolver, no local authority, an unknown
// operation), because a security boundary that half-starts is worse than one that
// refuses to.
func NewServer(local gomsg.Store, resolver IdentityResolver, ops OpSet, localAuthorities []string, opts ...ServerOption) (*Server, error) {
	if local == nil {
		return nil, errors.New("federation: NewServer needs a local Store")
	}
	if resolver == nil {
		return nil, errors.New("federation: NewServer needs an IdentityResolver")
	}
	ops, err := ops.validate()
	if err != nil {
		return nil, err
	}
	authz, err := newAuthorizer(localAuthorities)
	if err != nil {
		return nil, err
	}
	s := &Server{
		store: local, resolver: resolver, ops: ops, authz: authz,
		audit: func(AuditRecord) {}, maxPayload: DefaultMaxPayloadBytes, maxList: DefaultMaxListLimit,
		now: time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	s.handler = s.routes()
	return s, nil
}

// Ops returns the operations this server exposes.
func (s *Server) Ops() OpSet { return s.ops.Clone() }

// Handler is the HTTP handler: mount it behind a listener whose TLS config
// requires client certificates (Serve and Run check that).
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	base := BasePath
	// Only the routes of enabled operations are mounted. A disabled operation has
	// no route at all: Torque's "never mounted" behavior, as data. A GET for
	// .../inbox or .../subscribe therefore lands on the {id} route and is an
	// ordinary not-found.
	if s.ops.Allows(OpSend) {
		mux.HandleFunc("POST "+base, s.wrap(OpSend, s.handleSend))
		mux.HandleFunc("POST "+base+"/{$}", s.wrap(OpSend, s.handleSend))
	}
	if s.ops.Allows(OpThread) {
		mux.HandleFunc("GET "+base+"/thread/{thread_id}", s.wrap(OpThread, s.handleThread))
	}
	if s.ops.Allows(OpInbox) {
		mux.HandleFunc("GET "+base+"/inbox", s.wrap(OpInbox, s.handleInbox))
	}
	if s.ops.Allows(OpSubscribe) {
		mux.HandleFunc("GET "+base+"/subscribe", s.wrapStream(OpSubscribe, s.handleSubscribe))
	}
	// Get is always the {id} route, so it must exist for the not-found answers
	// above; when OpGet is off it answers 404 for every id without touching the
	// store.
	mux.HandleFunc("GET "+base+"/{id}", s.wrap(OpGet, s.handleGet))
	if s.ops.Allows(OpConsume) {
		mux.HandleFunc("POST "+base+"/{id}/consume", s.wrap(OpConsume, s.handleConsume))
	}
	if s.ops.Allows(OpCancel) {
		mux.HandleFunc("POST "+base+"/{id}/cancel", s.wrap(OpCancel, s.handleCancel))
	}
	return s.recoverer(mux)
}

// --- plumbing ---------------------------------------------------------------

type reqCtx struct {
	id Identity
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, c reqCtx)

// wrap authenticates the caller, bounds the response's write time and runs h.
// An operation that is off answers 404 before any identity is resolved or the
// store is touched.
func (s *Server) wrap(op Op, h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ops.Allows(op) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		id, ok := s.identify(w, r, op)
		if !ok {
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(s.now().Add(writeTimeout))
		h(w, r, reqCtx{id: id})
	}
}

// wrapStream is wrap for a long-lived response: no overall write deadline (the
// handler sets one per frame).
func (s *Server) wrapStream(op Op, h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ops.Allows(op) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		id, ok := s.identify(w, r, op)
		if !ok {
			return
		}
		h(w, r, reqCtx{id: id})
	}
}

// identify resolves the caller or writes the refusal. The response says only
// that the caller is unauthenticated or forbidden; why goes to the audit log.
func (s *Server) identify(w http.ResponseWriter, r *http.Request, op Op) (Identity, bool) {
	id, err := s.resolver.Resolve(r.Context(), r)
	if err == nil {
		return id, true
	}
	status := http.StatusForbidden
	if errors.Is(err, ErrUnauthenticated) {
		status = http.StatusUnauthorized
	}
	s.record(AuditRecord{Peer: "<unknown>", Op: op, Reason: "identity: " + err.Error()})
	writeError(w, status, http.StatusText(status))
	return Identity{}, false
}

func (s *Server) record(rec AuditRecord) {
	rec.Time = s.now()
	s.audit(rec)
}

func (s *Server) auditEnv(id Identity, op Op, env gomsg.Envelope, allowed bool, reason string) {
	s.record(AuditRecord{
		Peer: id.Label, Fingerprint: id.Fingerprint, Op: op, Authority: s.authz.governingAuthority(op, env),
		From: env.From.URN(), To: env.To.URN(), Allowed: allowed, Reason: reason,
	})
}

// reject maps an authorization error to its status and audits the denial. A
// trust-boundary failure is 403 and is not disguised as 404; an unroutable
// authority is 404. The caller gets a fixed phrase, never the error text.
func (s *Server) reject(w http.ResponseWriter, id Identity, op Op, env gomsg.Envelope, err error) {
	s.auditEnv(id, op, env, false, err.Error())
	if errors.Is(err, errNotHomed) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusForbidden, "forbidden")
}

// storeError maps a store failure. The text of an internal error can carry
// anything (paths, SQL); it is audited and never sent.
func (s *Server) storeError(w http.ResponseWriter, id Identity, op Op, env gomsg.Envelope, err error) {
	s.auditEnv(id, op, env, false, "store error: "+err.Error())
	switch {
	case errors.Is(err, gomsg.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, gomsg.ErrPresetLifecycle):
		writeError(w, http.StatusUnprocessableEntity, "envelope sets store-managed lifecycle fields")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { //nolint:errorlint // ErrAbortHandler is compared by identity, per net/http
					panic(v)
				}
				s.record(AuditRecord{Peer: "<unknown>", Reason: fmt.Sprintf("panic serving %s %s: %v", r.Method, r.URL.Path, v)})
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// --- handlers ---------------------------------------------------------------

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request, c reqCtx) {
	// The body holds the envelope around the payload (addresses, metadata, framing):
	// twice the payload cap plus slack for that.
	r.Body = http.MaxBytesReader(w, r.Body, int64(2*s.maxPayload+maxEnvelopeOverheadBytes))
	var env gomsg.Envelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.auditEnv(c.id, OpSend, gomsg.Envelope{}, false, "request body over the limit")
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid envelope JSON")
		return
	}
	switch {
	case env.Kind == "" || env.From.IsZero() || env.To.IsZero():
		writeError(w, http.StatusUnprocessableEntity, "envelope kind, from and to are required")
		return
	case len(env.Payload) > s.maxPayload:
		s.auditEnv(c.id, OpSend, env, false, "payload over the limit")
		writeError(w, http.StatusRequestEntityTooLarge, "envelope payload too large")
		return
	case usesReservedMetadata(env.Metadata):
		s.auditEnv(c.id, OpSend, env, false, "metadata uses the reserved "+ReservedMetadataPrefix+" namespace")
		writeError(w, http.StatusUnprocessableEntity, "metadata keys may not start with "+ReservedMetadataPrefix)
		return
	}
	if err := s.authz.authorizeSend(c.id, env); err != nil {
		s.reject(w, c.id, OpSend, env, err)
		return
	}
	out, err := s.store.Send(r.Context(), env)
	if err != nil {
		s.storeError(w, c.id, OpSend, env, err)
		return
	}
	s.auditEnv(c.id, OpSend, out, true, "delivered")
	writeJSON(w, http.StatusCreated, out)
}

func usesReservedMetadata(m map[string]string) bool {
	for k := range m {
		if strings.HasPrefix(k, ReservedMetadataPrefix) {
			return true
		}
	}
	return false
}

// fetchForAccess loads the {id} envelope and runs the access check for an
// operation on one envelope. It writes the refusal itself; the bool says whether
// to go on.
func (s *Server) fetchForAccess(w http.ResponseWriter, r *http.Request, op Op, id Identity) (gomsg.Envelope, bool) {
	msgID := r.PathValue("id")
	if msgID == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return gomsg.Envelope{}, false
	}
	env, err := s.store.Get(r.Context(), msgID)
	if err != nil {
		s.storeError(w, id, op, gomsg.Envelope{ID: msgID}, err)
		return gomsg.Envelope{}, false
	}
	if err := s.authz.authorizeEnvelopeAccess(op, id, env); err != nil {
		s.reject(w, id, op, env, err)
		return gomsg.Envelope{}, false
	}
	return env, true
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, c reqCtx) {
	env, ok := s.fetchForAccess(w, r, OpGet, c.id)
	if !ok {
		return
	}
	s.auditEnv(c.id, OpGet, env, true, "read")
	writeJSON(w, http.StatusOK, env)
}

func (s *Server) handleThread(w http.ResponseWriter, r *http.Request, c reqCtx) {
	threadID := r.PathValue("thread_id")
	if threadID == "" {
		writeError(w, http.StatusBadRequest, "missing thread id")
		return
	}
	filter, err := s.parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	envs, err := s.store.Thread(r.Context(), threadID, filter)
	if err != nil {
		s.storeError(w, c.id, OpThread, gomsg.Envelope{ThreadID: threadID}, err)
		return
	}
	visible := s.authz.threadView(c.id, envs)
	s.record(AuditRecord{Peer: c.id.Label, Fingerprint: c.id.Fingerprint, Op: OpThread, Allowed: true,
		Reason: fmt.Sprintf("thread %s: %d of %d envelopes visible to the caller", threadID, len(visible), len(envs))})
	writeJSON(w, http.StatusOK, map[string]any{"messages": visible})
}

func (s *Server) handleConsume(w http.ResponseWriter, r *http.Request, c reqCtx) {
	var req struct {
		Recipient string `json:"recipient"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConsumeBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Recipient == "" {
		writeError(w, http.StatusUnprocessableEntity, "recipient is required")
		return
	}
	recipient, err := gomsg.ParseURN(req.Recipient)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "recipient is not a valid URN")
		return
	}
	env, ok := s.fetchForAccess(w, r, OpConsume, c.id)
	if !ok {
		return
	}
	if err := s.authz.authorizeConsume(c.id, env, recipient); err != nil {
		s.reject(w, c.id, OpConsume, env, err)
		return
	}
	if err := s.store.Consume(r.Context(), env.ID, recipient); err != nil {
		s.storeError(w, c.id, OpConsume, env, err)
		return
	}
	s.auditEnv(c.id, OpConsume, env, true, "consumed by "+recipient.URN())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request, c reqCtx) {
	env, ok := s.fetchForAccess(w, r, OpCancel, c.id)
	if !ok {
		return
	}
	if err := s.store.Cancel(r.Context(), env.ID); err != nil {
		s.storeError(w, c.id, OpCancel, env, err)
		return
	}
	s.auditEnv(c.id, OpCancel, env, true, "canceled")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request, c reqCtx) {
	to, filter, ok := s.mailboxRequest(w, r, OpInbox, c.id)
	if !ok {
		return
	}
	envs, err := s.store.Inbox(r.Context(), to, filter)
	if err != nil {
		s.storeError(w, c.id, OpInbox, gomsg.Envelope{To: to}, err)
		return
	}
	s.auditEnv(c.id, OpInbox, gomsg.Envelope{To: to}, true, fmt.Sprintf("%d envelopes", len(envs)))
	writeJSON(w, http.StatusOK, map[string]any{"messages": envs})
}

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request, c reqCtx) {
	to, filter, ok := s.mailboxRequest(w, r, OpSubscribe, c.id)
	if !ok {
		return
	}
	rc := http.NewResponseController(w)
	ch, err := s.store.Subscribe(r.Context(), to, filter)
	if err != nil {
		s.storeError(w, c.id, OpSubscribe, gomsg.Envelope{To: to}, err)
		return
	}
	s.auditEnv(c.id, OpSubscribe, gomsg.Envelope{To: to}, true, "subscribed")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	write := func(frame string) bool {
		_ = rc.SetWriteDeadline(s.now().Add(streamWriteTimeout))
		if _, err := w.Write([]byte(frame)); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write(": subscribed\n\n") {
		return
	}
	tick := time.NewTicker(keepAliveInterval)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if !write(": keepalive\n\n") {
				return
			}
		case env, open := <-ch:
			if !open {
				return
			}
			b, err := json.Marshal(env)
			if err != nil {
				continue
			}
			if !write("data: " + string(b) + "\n\n") {
				return
			}
		}
	}
}

// mailboxRequest parses and authorizes the recipient and filter of an Inbox or
// Subscribe request.
func (s *Server) mailboxRequest(w http.ResponseWriter, r *http.Request, op Op, id Identity) (gomsg.Address, gomsg.Filter, bool) {
	raw := r.URL.Query().Get("to")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "to is required")
		return gomsg.Address{}, gomsg.Filter{}, false
	}
	to, err := gomsg.ParseURN(raw)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "to is not a valid URN")
		return gomsg.Address{}, gomsg.Filter{}, false
	}
	filter, err := s.parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return gomsg.Address{}, gomsg.Filter{}, false
	}
	if err := s.authz.authorizeMailbox(op, id, to); err != nil {
		s.reject(w, id, op, gomsg.Envelope{To: to}, err)
		return gomsg.Address{}, gomsg.Filter{}, false
	}
	return to, filter, true
}

// parseFilter reads the filter httpstore's Torque dialect encodes (repeated kind
// and channel, thread_id, limit). limit is clamped to the server's maximum, and
// applied when absent: no call returns an unbounded list.
func (s *Server) parseFilter(r *http.Request) (gomsg.Filter, error) {
	q := r.URL.Query()
	var f gomsg.Filter
	for _, v := range q["kind"] {
		if v != "" {
			f.Kind = append(f.Kind, gomsg.Kind(v))
		}
	}
	for _, v := range q["channel"] {
		if v != "" {
			f.Channel = append(f.Channel, gomsg.Channel(v))
		}
	}
	f.ThreadID = q.Get("thread_id")
	f.Limit = s.maxList
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return gomsg.Filter{}, errors.New("limit must be a non-negative integer")
		}
		if n > 0 && n < s.maxList {
			f.Limit = n
		}
	}
	return f, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// --- serving ----------------------------------------------------------------

// Run listens on addr and serves until ctx is canceled. tlsConf must be a
// ServerTLSConfig (or equivalent): one that requires client certificates.
func (s *Server) Run(ctx context.Context, addr string, tlsConf *tls.Config) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("federation: listen %s: %w", addr, err)
	}
	return s.Serve(ctx, ln, tlsConf)
}

// Serve serves on ln until ctx is canceled, then shuts down gracefully. It
// refuses a TLS config that does not require client certificates: without them
// no caller could be identified.
func (s *Server) Serve(ctx context.Context, ln net.Listener, tlsConf *tls.Config) error {
	if tlsConf == nil || (tlsConf.ClientAuth != tls.RequireAnyClientCert && tlsConf.ClientAuth != tls.RequireAndVerifyClientCert) {
		_ = ln.Close()
		return errors.New("federation: the listener's TLS config must require client certificates")
	}
	hs := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	go func() { //nolint:gosec // ctx is already done when the drain starts; it needs a fresh deadline
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hs.Shutdown(shutdown); err != nil {
			_ = hs.Close()
		}
	}()
	err := hs.Serve(tls.NewListener(ln, tlsConf))
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
