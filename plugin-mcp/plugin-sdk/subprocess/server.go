package subprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	plugin "github.com/hollis-labs/plugin-sdk"
	"github.com/hollis-labs/plugin-sdk/capability"
)

// Serve runs the JSON-RPC server loop against a plugin, reading
// requests from stdin and writing responses to stdout.
//
// The lifecycle is:
//
//  1. Serve installs a signal handler for SIGTERM/SIGINT so a host-
//     initiated shutdown triggers Unload before the process exits.
//  2. Serve reads newline-delimited JSON requests from stdin. Each
//     request is dispatched in its own goroutine with panic recovery.
//  3. Unknown methods return ErrCodeMethodNotFound.
//  4. Explicit unload, stdin EOF or a shutdown signal fences new work,
//     cancels/drains admitted work, then calls Unload at most once and returns.
//     The default shutdown budget is five seconds; see ServeWithOptions.
//
// The Plugin must implement the minimum Init/Load/Unload contract.
// Optional capability interfaces (CommandHandler, EventHandler,
// CRUDHandler, HealthChecker, Migrator, MCPHandler, HTTPHandler,
// IdentityAware) are detected via type assertion at startup.
//
// A plugin main typically looks like:
//
//	func main() {
//	    if err := subprocess.Serve(&myPlugin{}); err != nil {
//	        os.Exit(1)
//	    }
//	}
func Serve(p Plugin) error {
	return ServeWithOptions(p, ServeOptions{})
}

// serveWith preserves the package test entry point; injected streams stay caller-owned.
func serveWith(p Plugin, in io.Reader, out io.Writer) error {
	return ServeWithOptions(p, ServeOptions{Input: in, Output: out})
}

// server holds the per-invocation state for one Serve call.
type server struct {
	reverse             *reverseNegotiation
	outputBytes         func() int
	hooksEnabled        bool
	hooksFixtureEnabled bool
	hooksIncarnation    capability.RuntimeIdentity
	initMu              sync.Mutex
	initAttempted       bool
	initialized         bool
	plugin              Plugin
	writeFrame          func([]byte)
	publishResponse     func(RPCID, []byte)
	outputLimit         int
	fence               func(error)
	unloadOnce          sync.Once
	unloadDone          chan struct{}
	unloadErr           error
	logger              plugin.Logger
	secrets             *secretTracker

	// Capability flags — populated by detectCapabilities.
	asCommand  CommandHandler
	asEvent    EventHandler
	asCRUD     CRUDHandler
	asHealth   HealthChecker
	asMigrate  Migrator
	asMCP      MCPHandler
	asHTTP     HTTPHandler
	asIdentity IdentityAware
}

func (s *server) detectCapabilities() {
	if c, ok := s.plugin.(CommandHandler); ok {
		s.asCommand = c
	}
	if e, ok := s.plugin.(EventHandler); ok {
		s.asEvent = e
	}
	if c, ok := s.plugin.(CRUDHandler); ok {
		s.asCRUD = c
	}
	if h, ok := s.plugin.(HealthChecker); ok {
		s.asHealth = h
	}
	if m, ok := s.plugin.(Migrator); ok {
		s.asMigrate = m
	}
	if m, ok := s.plugin.(MCPHandler); ok {
		s.asMCP = m
	}
	if h, ok := s.plugin.(HTTPHandler); ok {
		s.asHTTP = h
	}
	if ia, ok := s.plugin.(IdentityAware); ok {
		s.asIdentity = ia
	}
}

// notifyIdentity delivers identity to the plugin's IdentityAware
// capability, if implemented — but only when the host actually
// populated a value. A plugin implementing IdentityAware never
// receives a call for a dispatch that carried no identity.
func (s *server) notifyIdentity(ctx context.Context, identity json.RawMessage) {
	if s.asIdentity == nil || len(identity) == 0 {
		return
	}
	s.asIdentity.Identity(ctx, identity)
}

// dispatch routes a single RPCRequest to the plugin. For notifications
// (req.ID == (RPCID{})) the response is suppressed.
func (s *server) dispatch(ctx context.Context, req RPCRequest) {
	if req.Method != MethodInit {
		s.initMu.Lock()
		ready := s.initialized
		s.initMu.Unlock()
		if !ready {
			s.writeError(req.ID, ErrCodeInvalidRequest, "successful init required", scopeFromContext(ctx))
			return
		}
	}
	if req.Method != MethodInit && s.supportsMethod(req.Method) {
		forward, err := validateRuntimeParams(req.Method, req.Params)
		if err != nil {
			s.writeError(req.ID, ErrCodeInvalidParams, err.Error(), scopeFromContext(ctx))
			return
		}
		ctx = withForwardContext(ctx, forward)
		ctx = s.reverse.client(ctx)
	}
	switch req.Method {
	case MethodHookHandle, MethodHookHandleBatch:
		s.dispatchHook(ctx, req)
	case MethodInit:
		if !req.ID.positiveInteger() {
			s.writeError(req.ID, ErrCodeInvalidRequest, "init requires a positive safe request ID", scopeFromContext(ctx))
			return
		}
		s.initMu.Lock()
		if s.initAttempted {
			s.initMu.Unlock()
			s.writeError(req.ID, ErrCodeInvalidRequest, "init already attempted", scopeFromContext(ctx))
			return
		}
		s.initAttempted = true
		s.initMu.Unlock()
		var params InitParams
		if err := decodeParams(req.Params, &params); err != nil {
			s.writeInitError(req.ID, err, scopeFromContext(ctx))
			return
		}
		if err := params.Validate(); err != nil {
			s.writeInitError(req.ID, err, scopeFromContext(ctx))
			return
		}
		var agreement InitParams
		if err := decodeParams(req.Params, &agreement); err != nil {
			s.writeInitError(req.ID, err, scopeFromContext(ctx))
			return
		}
		ctx = withForwardContext(ctx, params.Context)
		ctx, err := s.reverse.prepare(ctx, params)
		if err != nil {
			s.writeInitError(req.ID, err, scopeFromContext(ctx))
			return
		}
		accepted := false
		defer func() {
			if !accepted {
				s.reverse.decline()
			}
		}()
		_, hookHandler := s.plugin.(HookHandler)
		hooksEnabled := params.HooksProfile != nil && params.HooksProfile.HooksProfileVersion == HooksProfileVersion && hookHandler
		res, err := s.plugin.Init(ctx, params)
		if err != nil {
			s.reverse.decline()
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		if scope := scopeFromContext(ctx); scope != nil && !scope.acceptsResult() {
			s.reverse.decline()
			scope.failContext()
			return
		}
		// The runtime owns both independent acknowledgements.
		res.ReverseRPCVersion = nil
		if s.reverse.selected() {
			version := 1
			res.ReverseRPCVersion = &version
		}
		res.HooksProfileVersion = nil
		if hooksEnabled {
			version := HooksProfileVersion
			res.HooksProfileVersion = &version
		}
		if err := ValidateInitResult(agreement, res); err != nil {
			s.reverse.decline()
			s.writeInitError(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.notifyIdentity(ctx, agreement.Identity)
		commit := func(resp RPCResponse) {
			if resp.Error != nil {
				s.reverse.decline()
				return
			}
			s.initMu.Lock()
			s.reverse.activate(req.ID)
			s.hooksEnabled = hooksEnabled
			s.hooksIncarnation = agreement.Incarnation
			s.initialized = true
			s.initMu.Unlock()
		}
		if scope := scopeFromContext(ctx); scope != nil {
			if !scope.acceptsResult() || !scope.observeReply(commit) {
				s.reverse.decline()
				scope.failContext()
				return
			}
		} else {
			commit(RPCResponse{})
		}
		accepted = true
		s.writeResult(req.ID, res, scopeFromContext(ctx))

	case MethodLoad:
		res, err := s.plugin.Load(ctx)
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.writeResult(req.ID, res, scopeFromContext(ctx))

	case MethodUnload:
		if err := s.unload(ctx); err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.writeResult(req.ID, map[string]bool{"ok": true}, scopeFromContext(ctx))

	case MethodHealth:
		if s.asHealth == nil {
			// Default: healthy.
			s.writeResult(req.ID, HealthResult{OK: true}, scopeFromContext(ctx))
			return
		}
		status, err := s.asHealth.Health(ctx)
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.writeResult(req.ID, HealthResult{OK: status.OK, Message: status.Message}, scopeFromContext(ctx))

	case MethodCommandExecute:
		if s.asCommand == nil {
			s.writeError(req.ID, ErrCodeMethodNotFound, "plugin does not implement CommandHandler", scopeFromContext(ctx))
			return
		}
		var params CommandExecParams
		if err := decodeParams(req.Params, &params); err != nil {
			s.writeError(req.ID, ErrCodeInvalidParams, err.Error(), scopeFromContext(ctx))
			return
		}
		s.notifyIdentity(ctx, params.Identity)
		res, err := s.asCommand.Command(ctx, CommandRequest{
			Name:      params.Name,
			SessionID: params.SessionID,
			Args:      params.Args,
			Identity:  params.Identity,
		})
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.writeResult(req.ID, CommandExecResult{Action: res.Action, Content: res.Content, Envelopes: res.Envelopes}, scopeFromContext(ctx))

	case MethodEventHandle:
		if s.asEvent == nil {
			// Notifications with no handler: silently drop (no error
			// back for fire-and-forget).
			if req.ID == (RPCID{}) {
				return
			}
			s.writeError(req.ID, ErrCodeMethodNotFound, "plugin does not implement EventHandler", scopeFromContext(ctx))
			return
		}
		var params EventHandleParams
		if err := decodeParams(req.Params, &params); err != nil {
			if req.ID != (RPCID{}) {
				s.writeError(req.ID, ErrCodeInvalidParams, err.Error(), scopeFromContext(ctx))
			}
			return
		}
		s.notifyIdentity(ctx, params.Identity)
		res, err := s.asEvent.EventHandle(ctx, EventRequest{
			Type:      params.Type,
			Source:    params.Source,
			Data:      params.Data,
			SessionID: params.SessionID,
			PreHook:   params.PreHook,
			Identity:  params.Identity,
		})
		if req.ID == (RPCID{}) {
			return // notification — drop response
		}
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.writeResult(req.ID, EventHandleResult{Cancel: res.Cancel, Reason: res.Reason, Envelopes: res.Envelopes}, scopeFromContext(ctx))

	case MethodCRUDCreate, MethodCRUDRead, MethodCRUDUpdate, MethodCRUDDelete, MethodCRUDList:
		if s.asCRUD == nil {
			s.writeError(req.ID, ErrCodeMethodNotFound, "plugin does not implement CRUDHandler", scopeFromContext(ctx))
			return
		}
		s.dispatchCRUD(ctx, req)

	case MethodMCPCallTool:
		if s.asMCP == nil {
			s.writeError(req.ID, ErrCodeMethodNotFound, "plugin does not implement MCPHandler", scopeFromContext(ctx))
			return
		}
		var params MCPCallRequest
		if err := decodeParams(req.Params, &params); err != nil {
			s.writeError(req.ID, ErrCodeInvalidParams, err.Error(), scopeFromContext(ctx))
			return
		}
		s.notifyIdentity(ctx, params.Identity)
		res, err := s.asMCP.MCPCallTool(ctx, params)
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.writeResult(req.ID, res, scopeFromContext(ctx))

	case MethodHTTPHandle:
		if s.asHTTP == nil {
			s.writeError(req.ID, ErrCodeMethodNotFound, "plugin does not implement HTTPHandler", scopeFromContext(ctx))
			return
		}
		var params HTTPRequest
		if err := decodeParams(req.Params, &params); err != nil {
			s.writeError(req.ID, ErrCodeInvalidParams, err.Error(), scopeFromContext(ctx))
			return
		}
		s.notifyIdentity(ctx, params.Identity)
		res, err := s.asHTTP.HTTPHandle(ctx, params)
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.writeResult(req.ID, res, scopeFromContext(ctx))

	case MethodMigrate:
		if s.asMigrate == nil {
			s.writeError(req.ID, ErrCodeMethodNotFound, "plugin does not implement Migrator", scopeFromContext(ctx))
			return
		}
		var params MigrateParams
		if err := decodeParams(req.Params, &params); err != nil {
			s.writeError(req.ID, ErrCodeInvalidParams, err.Error(), scopeFromContext(ctx))
			return
		}
		if err := s.asMigrate.Migrate(ctx, params.FromVersion, params.ToVersion); err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.writeResult(req.ID, MigrateResult{}, scopeFromContext(ctx))

	default:
		s.writeError(req.ID, ErrCodeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method), scopeFromContext(ctx))
	}
}

func (s *server) dispatchCRUD(ctx context.Context, req RPCRequest) {
	var params CRUDParams
	if err := decodeParams(req.Params, &params); err != nil {
		s.writeError(req.ID, ErrCodeInvalidParams, err.Error(), scopeFromContext(ctx))
		return
	}
	if req.Method == MethodCRUDList && params.Filters == nil {
		params.Filters = map[string]interface{}{}
	}
	switch req.Method {
	case MethodCRUDCreate:
		out, err := s.asCRUD.Create(ctx, params.ResourceType, params.Data)
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.writeResultShape(req.ID, map[string]any{"data": out}, CRUDResult{}, scopeFromContext(ctx))
	case MethodCRUDRead:
		out, err := s.asCRUD.Read(ctx, params.ResourceType, params.ID)
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.writeResultShape(req.ID, map[string]any{"data": out}, CRUDResult{}, scopeFromContext(ctx))
	case MethodCRUDUpdate:
		out, err := s.asCRUD.Update(ctx, params.ResourceType, params.ID, params.Data)
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.writeResultShape(req.ID, map[string]any{"data": out}, CRUDResult{}, scopeFromContext(ctx))
	case MethodCRUDDelete:
		if err := s.asCRUD.Delete(ctx, params.ResourceType, params.ID); err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		s.writeResult(req.ID, map[string]bool{"ok": true}, scopeFromContext(ctx))
	case MethodCRUDList:
		items, err := s.asCRUD.List(ctx, params.ResourceType, params.Filters)
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
			return
		}
		if items == nil {
			items = []map[string]interface{}{}
		}
		s.writeResultShape(req.ID, map[string]any{"items": items}, CRUDListResult{}, scopeFromContext(ctx))
	}
}

// writeResult encodes and writes a successful JSON-RPC response.
// Writes are suppressed for notifications (absent ID).
func (s *server) writeResult(id RPCID, result any, scopes ...*requestScope) {
	s.writeResultShape(id, result, result, scopes...)
}
func (s *server) writeResultShape(id RPCID, result, shape any, scopes ...*requestScope) {
	if id == (RPCID{}) {
		return
	}
	if err := payloadResultSource(result); err != nil {
		s.writeError(id, ErrCodeInternal, "marshal result: invalid JSON value", scopes...)
		return
	}
	payload, err := marshalBounded(result, s.frameOutputLimit()-1)
	if err == nil {
		err = validateRuntimeResult(shape, payload)
	}
	if err != nil {
		var sizeError *FrameTooLargeError
		if len(scopes) > 0 && scopes[0] != nil && errors.As(err, &sizeError) {
			scopes[0].reply(requestFailureResponse(id, capability.BudgetExceeded, capability.Committed))
		} else {
			s.writeError(id, ErrCodeInternal, "outbound response rejected", scopes...)
		}
		return
	}
	resp := RPCResponse{JSONRPC: "2.0", ID: id, Result: payload}
	s.writeMessage(resp, scopes...)
}

// writeError encodes and writes a JSON-RPC error response.
func (s *server) writeError(id RPCID, code int, message string, scopes ...*requestScope) {
	if id == (RPCID{}) {
		return
	}
	resp := RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: message},
	}
	s.writeMessage(resp, scopes...)
}

// writeInitError preserves the typed structural cause across the wire.
func (s *server) writeInitError(id RPCID, err error, scopes ...*requestScope) {
	if id == (RPCID{}) {
		return
	}
	var failure *InitError
	if !errors.As(err, &failure) {
		failure = &InitError{Code: InitInvalid, Field: "params"}
	}
	s.writeMessage(RPCResponse{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: ErrCodeInvalidParams, Message: failure.Error(), Data: failure.RPCData()}}, scopes...)
}

// writeErrorFromPluginErr maps a plugin.Error (with HTTP-style code) to
// the appropriate JSON-RPC application-level error code. Unknown error
// types fall back to ErrCodeInternal.
func (s *server) writeErrorFromPluginErr(id RPCID, err error, scopes ...*requestScope) {
	if id == (RPCID{}) {
		return
	}
	if errors.Is(err, plugin.ErrCancelled) {
		s.writeError(id, ErrCodeCancelled, err.Error(), scopes...)
		return
	}
	var pe *plugin.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case 404:
			s.writeError(id, ErrCodeNotFound, pe.Message, scopes...)
		case 409:
			s.writeError(id, ErrCodeConflict, pe.Message, scopes...)
		case 422:
			s.writeError(id, ErrCodeValidation, pe.Message, scopes...)
		default:
			s.writeError(id, ErrCodeInternal, pe.Message, scopes...)
		}
		return
	}
	s.writeError(id, ErrCodeInternal, err.Error(), scopes...)
}

// writeMessage serializes a response and writes it followed by a
// newline. The runtime writer serializes frames from concurrent handlers.
func (s *server) writeMessage(resp RPCResponse, scopes ...*requestScope) {
	if len(scopes) > 0 && scopes[0] != nil {
		scopes[0].reply(resp)
		return
	}
	data, err := s.encodeFrame(resp)
	if err != nil {
		fallback := RPCResponse{JSONRPC: "2.0", ID: resp.ID, Error: &RPCError{Code: ErrCodeInternal, Message: "outbound response rejected"}}
		data, err = s.encodeFrame(fallback)
		if err != nil {
			if s.fence != nil {
				s.fence(err)
			}
			return
		}
	}
	if s.publishResponse != nil {
		s.publishResponse(resp.ID, data)
	} else {
		s.emit(data)
	}
}
func (s *server) frameOutputLimit() int {
	if s.outputBytes != nil {
		return s.outputBytes()
	}
	if s.outputLimit == 0 {
		return DefaultFrameBytes
	}
	return s.outputLimit
}

func (s *server) emit(data []byte) { s.writeFrame(data) }

// decodeParams unmarshals req.Params (which is typed any from the
// decoded RPCRequest) into a concrete struct. Params decoded from the wire
// retain raw JSON tokens; programmatic requests may supply ordinary values.
func decodeParams(raw any, dst any) error {
	if raw == nil {
		return nil
	}
	b, err := paramsJSON(raw)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	return nil
}

// NewDataHelper returns a DataHelper rooted at the given directory.
// Typically called from a plugin's Init method with a path derived
// from InitParams (e.g., filepath.Join(params.PluginDir, "data")).
func NewDataHelper(dir string) DataHelper { return &dirHelper{root: dir} }

// NewCacheHelper returns a CacheHelper rooted at the given directory.
func NewCacheHelper(dir string) CacheHelper { return &dirHelper{root: dir} }

// NewConfigReader wraps a resolved config map and the package-level
// secret tracker so Secret lookups feed into log redaction.
func NewConfigReader(values map[string]string) ConfigReader {
	// When called outside Serve (e.g., in a test harness), wire to the
	// package logger's secret tracker so Secret lookups still work.
	var secrets *secretTracker
	if sl, ok := Log().(*stderrLogger); ok {
		secrets = sl.secrets
	} else {
		secrets = newSecretTracker()
	}
	return newConfigReader(values, secrets)
}

func (s *server) supportsMethod(method string) bool {
	switch method {
	case MethodLoad, MethodUnload, MethodHealth:
		return true
	case MethodCommandExecute:
		return s.asCommand != nil
	case MethodEventHandle:
		return s.asEvent != nil
	case MethodCRUDCreate, MethodCRUDRead, MethodCRUDUpdate, MethodCRUDDelete, MethodCRUDList:
		return s.asCRUD != nil
	case MethodMCPCallTool:
		return s.asMCP != nil
	case MethodHTTPHandle:
		return s.asHTTP != nil
	case MethodMigrate:
		return s.asMigrate != nil
	}
	return false
}
