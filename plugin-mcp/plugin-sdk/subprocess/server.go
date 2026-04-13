package subprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"

	plugin "github.com/hollis-labs/plugin-sdk"
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
//  4. Stdin EOF or the shutdown signal ends the loop; Serve calls
//     Unload on the plugin and returns.
//
// The Plugin must implement the minimum Init/Load/Unload contract.
// Optional capability interfaces (CommandHandler, EventHandler,
// CRUDHandler, HealthChecker, Migrator) are detected via type
// assertion at startup.
//
// A plugin main typically looks like:
//
//	func main() {
//	    if err := subprocess.Serve(&myPlugin{}); err != nil {
//	        os.Exit(1)
//	    }
//	}
func Serve(p Plugin) error {
	return serveWith(p, os.Stdin, os.Stdout)
}

// serveWith is the testable entry point. Production code calls Serve,
// which wires in os.Stdin/os.Stdout.
func serveWith(p Plugin, in io.Reader, out io.Writer) error {
	if p == nil {
		return errors.New("subprocess: Serve called with nil plugin")
	}

	secrets := newSecretTracker()
	logger := newStderrLogger(secrets)
	setPackageLogger(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal-triggered shutdown.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)

	srv := &server{
		plugin:  p,
		out:     out,
		logger:  logger,
		secrets: secrets,
	}
	srv.detectCapabilities()

	go func() {
		<-sigs
		logger.Info("subprocess: shutdown signal received")
		cancel()
	}()

	scanner := bufio.NewScanner(in)
	// Accept large JSON payloads (default 64KB is small for CRUD
	// list responses or bulk event data).
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	// Serialize writes so concurrent goroutines do not interleave.
	var writeMu sync.Mutex
	srv.writeMu = &writeMu

	// Track outstanding request goroutines so Serve waits for them
	// before calling Unload and returning.
	var wg sync.WaitGroup

	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := append([]byte{}, scanner.Bytes()...)
			var req RPCRequest
			if err := json.Unmarshal(line, &req); err != nil {
				// Parse errors must always surface per JSON-RPC 2.0 —
				// write directly with id=0 bypassing the notification
				// suppression in writeError.
				srv.writeMessage(RPCResponse{
					JSONRPC: "2.0",
					Error:   &RPCError{Code: ErrCodeParse, Message: fmt.Sprintf("parse error: %v", err)},
				})
				continue
			}

			wg.Add(1)
			go func(req RPCRequest) {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						logger.Error("subprocess: panic in handler",
							"method", req.Method,
							"panic", fmt.Sprint(r),
							"stack", string(debug.Stack()),
						)
						srv.writeError(req.ID, ErrCodeInternal, fmt.Sprintf("panic: %v", r))
					}
				}()
				srv.dispatch(ctx, req)
			}(req)
		}
	}()

	select {
	case <-done:
		// stdin closed — normal shutdown. Fall through.
	case <-ctx.Done():
		// Signal-triggered shutdown. Close stdin would require a
		// per-platform trick; instead we just wait for in-flight
		// requests to complete via ctx cancellation.
	}

	wg.Wait()

	// Final Unload.
	unloadCtx, unloadCancel := context.WithCancel(context.Background())
	defer unloadCancel()
	if err := p.Unload(unloadCtx); err != nil {
		logger.Error("subprocess: unload returned error", "err", err.Error())
		return err
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("stdin scanner: %w", err)
	}
	return nil
}

// server holds the per-invocation state for one Serve call.
type server struct {
	plugin  Plugin
	out     io.Writer
	writeMu *sync.Mutex
	logger  plugin.Logger
	secrets *secretTracker

	// Capability flags — populated by detectCapabilities.
	asCommand CommandHandler
	asEvent   EventHandler
	asCRUD    CRUDHandler
	asHealth  HealthChecker
	asMigrate Migrator
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
}

// dispatch routes a single RPCRequest to the plugin. For notifications
// (req.ID == 0) the response is suppressed.
func (s *server) dispatch(ctx context.Context, req RPCRequest) {
	switch req.Method {
	case MethodInit:
		var params InitParams
		if err := decodeParams(req.Params, &params); err != nil {
			s.writeError(req.ID, ErrCodeInvalidParams, err.Error())
			return
		}
		res, err := s.plugin.Init(ctx, params)
		if err != nil {
			s.writeError(req.ID, ErrCodeInternal, err.Error())
			return
		}
		s.writeResult(req.ID, res)

	case MethodLoad:
		res, err := s.plugin.Load(ctx)
		if err != nil {
			s.writeError(req.ID, ErrCodeInternal, err.Error())
			return
		}
		s.writeResult(req.ID, res)

	case MethodUnload:
		if err := s.plugin.Unload(ctx); err != nil {
			s.writeError(req.ID, ErrCodeInternal, err.Error())
			return
		}
		s.writeResult(req.ID, map[string]bool{"ok": true})

	case MethodHealth:
		if s.asHealth == nil {
			// Default: healthy.
			s.writeResult(req.ID, HealthResult{OK: true})
			return
		}
		status, err := s.asHealth.Health(ctx)
		if err != nil {
			s.writeResult(req.ID, HealthResult{OK: false, Message: err.Error()})
			return
		}
		s.writeResult(req.ID, HealthResult{OK: status.OK, Message: status.Message})

	case MethodCommandExecute:
		if s.asCommand == nil {
			s.writeError(req.ID, ErrCodeMethodNotFound, "plugin does not implement CommandHandler")
			return
		}
		var params CommandExecParams
		if err := decodeParams(req.Params, &params); err != nil {
			s.writeError(req.ID, ErrCodeInvalidParams, err.Error())
			return
		}
		res, err := s.asCommand.Command(ctx, CommandRequest{
			Name:      params.Name,
			SessionID: params.SessionID,
			Args:      params.Args,
		})
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err)
			return
		}
		s.writeResult(req.ID, CommandExecResult{Action: res.Action, Content: res.Content, Envelopes: res.Envelopes})

	case MethodEventHandle:
		if s.asEvent == nil {
			// Notifications with no handler: silently drop (no error
			// back for fire-and-forget).
			if req.ID == 0 {
				return
			}
			s.writeError(req.ID, ErrCodeMethodNotFound, "plugin does not implement EventHandler")
			return
		}
		var params EventHandleParams
		if err := decodeParams(req.Params, &params); err != nil {
			if req.ID != 0 {
				s.writeError(req.ID, ErrCodeInvalidParams, err.Error())
			}
			return
		}
		res, err := s.asEvent.EventHandle(ctx, EventRequest{
			Type:      params.Type,
			Source:    params.Source,
			Data:      params.Data,
			SessionID: params.SessionID,
			PreHook:   params.PreHook,
		})
		if req.ID == 0 {
			return // notification — drop response
		}
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err)
			return
		}
		s.writeResult(req.ID, EventHandleResult{Cancel: res.Cancel, Reason: res.Reason, Envelopes: res.Envelopes})

	case MethodCRUDCreate, MethodCRUDRead, MethodCRUDUpdate, MethodCRUDDelete, MethodCRUDList:
		if s.asCRUD == nil {
			s.writeError(req.ID, ErrCodeMethodNotFound, "plugin does not implement CRUDHandler")
			return
		}
		s.dispatchCRUD(ctx, req)

	default:
		s.writeError(req.ID, ErrCodeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method))
	}
}

func (s *server) dispatchCRUD(ctx context.Context, req RPCRequest) {
	var params CRUDParams
	if err := decodeParams(req.Params, &params); err != nil {
		s.writeError(req.ID, ErrCodeInvalidParams, err.Error())
		return
	}
	switch req.Method {
	case MethodCRUDCreate:
		out, err := s.asCRUD.Create(ctx, params.ResourceType, params.Data)
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err)
			return
		}
		data, _ := json.Marshal(out)
		s.writeResult(req.ID, CRUDResult{Data: data})
	case MethodCRUDRead:
		out, err := s.asCRUD.Read(ctx, params.ResourceType, params.ID)
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err)
			return
		}
		data, _ := json.Marshal(out)
		s.writeResult(req.ID, CRUDResult{Data: data})
	case MethodCRUDUpdate:
		out, err := s.asCRUD.Update(ctx, params.ResourceType, params.ID, params.Data)
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err)
			return
		}
		data, _ := json.Marshal(out)
		s.writeResult(req.ID, CRUDResult{Data: data})
	case MethodCRUDDelete:
		if err := s.asCRUD.Delete(ctx, params.ResourceType, params.ID); err != nil {
			s.writeErrorFromPluginErr(req.ID, err)
			return
		}
		s.writeResult(req.ID, map[string]bool{"ok": true})
	case MethodCRUDList:
		items, err := s.asCRUD.List(ctx, params.ResourceType, params.Filters)
		if err != nil {
			s.writeErrorFromPluginErr(req.ID, err)
			return
		}
		raw := make([]json.RawMessage, 0, len(items))
		for _, it := range items {
			b, _ := json.Marshal(it)
			raw = append(raw, b)
		}
		s.writeResult(req.ID, CRUDListResult{Items: raw})
	}
}

// writeResult encodes and writes a successful JSON-RPC response.
// Writes are suppressed for notifications (id == 0).
func (s *server) writeResult(id int64, result any) {
	if id == 0 {
		return
	}
	payload, err := json.Marshal(result)
	if err != nil {
		s.writeError(id, ErrCodeInternal, fmt.Sprintf("marshal result: %v", err))
		return
	}
	resp := RPCResponse{JSONRPC: "2.0", ID: id, Result: payload}
	s.writeMessage(resp)
}

// writeError encodes and writes a JSON-RPC error response.
func (s *server) writeError(id int64, code int, message string) {
	if id == 0 {
		return
	}
	resp := RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: message},
	}
	s.writeMessage(resp)
}

// writeErrorFromPluginErr maps a plugin.Error (with HTTP-style code) to
// the appropriate JSON-RPC application-level error code. Unknown error
// types fall back to ErrCodeInternal.
func (s *server) writeErrorFromPluginErr(id int64, err error) {
	if id == 0 {
		return
	}
	if errors.Is(err, plugin.ErrCancelled) {
		s.writeError(id, ErrCodeCancelled, err.Error())
		return
	}
	var pe *plugin.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case 404:
			s.writeError(id, ErrCodeNotFound, pe.Message)
		case 409:
			s.writeError(id, ErrCodeConflict, pe.Message)
		case 422:
			s.writeError(id, ErrCodeValidation, pe.Message)
		default:
			s.writeError(id, ErrCodeInternal, pe.Message)
		}
		return
	}
	s.writeError(id, ErrCodeInternal, err.Error())
}

// writeMessage serializes a response and writes it followed by a
// newline. Writes are protected by writeMu so concurrent goroutines do
// not interleave bytes on stdout.
func (s *server) writeMessage(resp RPCResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		// Last-resort: write a minimal parse-error frame. We cannot
		// recurse into writeError because json.Marshal already failed.
		fallback := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":%d,"message":"marshal failed: %s"}}`+"\n",
			resp.ID, ErrCodeInternal, err.Error())
		s.writeMu.Lock()
		_, _ = s.out.Write([]byte(fallback))
		s.writeMu.Unlock()
		return
	}
	data = append(data, '\n')
	s.writeMu.Lock()
	_, _ = s.out.Write(data)
	s.writeMu.Unlock()
}

// decodeParams unmarshals req.Params (which is typed any from the
// decoded RPCRequest) into a concrete struct. Because the outer
// Unmarshal left Params as an interface{} (usually map[string]any), we
// re-marshal and re-unmarshal; this is cheap for small payloads and
// keeps the dispatch layer simple.
func decodeParams(raw any, dst any) error {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
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
