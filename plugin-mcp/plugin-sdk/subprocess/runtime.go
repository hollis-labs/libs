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
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
	"github.com/hollis-labs/plugin-sdk/internal/strictjson"
)

// DefaultShutdownTimeout bounds drain, cleanup and flush together.
const DefaultShutdownTimeout = 5 * time.Second

// ErrShutdownTimeout means drain, cleanup or output flushing did not finish.
// It is a transport/cleanup failure, not the plugin's hook-veto cancellation.
var ErrShutdownTimeout = errors.New("subprocess: shutdown incomplete: deadline exceeded")

// ServeOptions controls one connection. Zero values select stdin/stdout,
// background context and DefaultShutdownTimeout. Injected I/O remains owned by
// the caller: close it to release any Read/Write still blocked after shutdown.
// Serve cannot interrupt arbitrary injected I/O or kill callback goroutines.
type ServeOptions struct {
	Input           io.Reader
	Output          io.Writer
	Context         context.Context
	ShutdownTimeout time.Duration
	FrameLimits     FrameLimits
	QueueLimits     QueueLimits
	AdmissionLimits AdmissionLimits
	WriteTimeout    time.Duration
}

// ServeWithOptions shares one shutdown path for unload, EOF and cancellation.
// Only runtime-owned stdin/stdout may be closed to interrupt pending I/O.
func ServeWithOptions(p Plugin, options ServeOptions) error {
	return serveConnection(p, options, newCorrelation(false))
}

func serveConnection(p Plugin, options ServeOptions, core *correlation) error {
	if p == nil {
		return errors.New("subprocess: Serve called with nil plugin")
	}
	limits, err := frameLimits(options.FrameLimits)
	if err != nil {
		return err
	}
	queues, err := queueLimits(options.QueueLimits)
	if err != nil {
		return err
	}
	admissionPolicy, err := admissionLimits(options.AdmissionLimits)
	if err != nil {
		return err
	}
	core.reversePermits = make(chan struct{}, admissionPolicy.Reverse)
	timeout := options.ShutdownTimeout
	if timeout == 0 {
		timeout = DefaultShutdownTimeout
	}
	if timeout < 0 {
		return errors.New("subprocess: shutdown timeout must be positive")
	}
	writeTimeout := options.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = DefaultWriteTimeout
	}
	if writeTimeout < 0 {
		return errors.New("subprocess: write timeout must be positive")
	}
	parent := options.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	in, out := options.Input, options.Output
	ownInput, ownOutput := in == nil, out == nil
	if ownInput {
		in = os.Stdin
	}
	if ownOutput {
		out = os.Stdout
	}
	if ownInput {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(sigs)
		go func() {
			select {
			case <-sigs:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	secrets := newSecretTracker()
	logger := newStderrLogger(secrets)
	setPackageLogger(logger)
	srv := &server{plugin: p, logger: logger, secrets: secrets, unloadDone: make(chan struct{}), outputLimit: limits.OutputBytes}
	srv.detectCapabilities()
	if hookFixtureSetup != nil {
		hookFixtureSetup(srv)
	}
	fatal := make(chan error, 1)
	srv.fence = func(err error) {
		select {
		case fatal <- err:
		default:
		}
		cancel()
	}

	// Physical writes retain 3d bounds/timeouts. Correlation owns publication receipts.
	writer := newFrameWriterWithLimits(out, writeTimeout, func() {
		if ownOutput {
			_ = os.Stdout.Close()
		}
	}, queues)
	writer.onFailure = func(err error) { core.close(err); srv.fence(err) }
	defer func() { writer.abort(errConnectionClosed); core.close(errConnectionClosed) }()
	core.encode = srv.encodeFrame
	core.publish = writer.submit
	core.publishCall = writer.submitCancellable
	core.publishControl = writer.submitControl
	srv.writeFrame = func(data []byte) {
		if err := writer.submitControl(data, nil); err != nil {
			srv.fence(err)
		}
	}
	srv.publishResponse = func(id RPCID, data []byte) {
		err := writer.submitControl(data, func(err error) {
			core.release(id)
			if err != nil {
				core.close(err)
				srv.fence(err)
			}
		})
		if err != nil {
			core.release(id)
			core.close(err)
			srv.fence(err)
		}
	}
	admissions := newAdmission(writer, core, srv)
	admissions.limits = admissionPolicy
	writerDone := writer.done
	stopRead := make(chan struct{})
	defer close(stopRead)
	type arrival struct {
		raw      []byte
		received time.Time
	}
	events := make(chan arrival)
	readerDone := make(chan struct{})
	var readerErr error
	go func() {
		defer close(readerDone)
		reader := bufio.NewReaderSize(in, 64*1024)
		for {
			line, err := readFrame(reader, limits.InputBytes)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					readerErr = err
				}
				return
			}
			select {
			case events <- arrival{line, time.Now()}:
			case <-stopRead:
				return
			}
		}
	}()
	var pending sync.WaitGroup
	run := func(scope *requestScope, call func()) <-chan struct{} {
		done := make(chan struct{})
		pending.Add(1)
		go func() {
			defer pending.Done()
			defer close(done)
			defer func() { scope.finish(); <-scope.executionDone }()
			if scope.start() {
				call()
			}
		}()
		return done
	}
	dispatch := func(req RPCRequest, scope *requestScope) <-chan struct{} {
		return run(scope, func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("subprocess: panic in handler", "method", req.Method, "stack", string(debug.Stack()))
					srv.writeError(req.ID, ErrCodeInternal, fmt.Sprintf("panic: %v", r), scope)
				}
			}()
			srv.dispatch(scope.ctx, req)
		})
	}
	initUsed := false
	var terminal *RPCRequest
	var terminalScope *requestScope
	terminalReady := false
	var inputErr error
loop:
	for {
		input := events

		select {
		case <-ctx.Done():
			select {
			case inputErr = <-fatal:
			default:
			}
			break loop
		case failure := <-fatal:
			inputErr = failure
			break loop
		case <-writerDone:
			inputErr = writer.failure()
			break loop
		case <-readerDone:
			inputErr = readerErr
			break loop
		case event := <-input:
			line := event.raw
			req, fault := decodeEnvelope(line)
			if fault != nil {
				if core.directional && (!json.Valid(line) || replyCandidate(line)) {
					inputErr = errCorrelation
					break loop
				}
				data, err := srv.encodeFrame(*fault)
				if err != nil {
					inputErr = err
					break loop
				}
				if err = writer.submitControl(data, nil); err != nil {
					inputErr = err
					break loop
				}
				continue
			}
			if req == nil {
				if core.directional {
					if err := core.reply(line); err != nil {
						inputErr = err
						break loop
					}
				}
				continue
			}
			if req.Method == "rpc/cancel" {
				if req.ID != (RPCID{}) {
					if err := core.admit(req.ID); err != nil {
						inputErr = err
						break loop
					}
					srv.writeError(req.ID, ErrCodeInvalidRequest, "rpc/cancel requires a notification")
					continue
				}
				raw, err := paramsJSON(req.Params)
				if err == nil && ValidateRPCControlDTO("CancelParams", raw, core.directional) == nil {
					var p CancelParams
					_ = json.Unmarshal(raw, &p)
					admissions.cancelRequest(p)
				}
				continue
			}
			if err := core.admit(req.ID); err != nil {
				inputErr = err
				break loop
			}
			scopeParent := ctx
			if req.Method == MethodUnload {
				scopeParent = context.Background()
			}
			scope, admitErr := admissions.begin(scopeParent, *req, event.received)
			if admitErr != nil {
				if req.ID != (RPCID{}) {
					data, err := srv.encodeFrame(requestFailureResponse(req.ID, capability.RateLimited, capability.NotStarted))
					if err != nil {
						inputErr = err
						break loop
					}
					id := req.ID
					if err = writer.submitControl(data, func(err error) {
						core.release(id)
						if err != nil {
							srv.fence(err)
						}
					}); err != nil {
						inputErr = err
						break loop
					}
				}
				continue
			}
			srv.initMu.Lock()
			ready := srv.initialized
			srv.initMu.Unlock()
			if req.Method == MethodUnload {
				if _, err := validateRuntimeParams(req.Method, req.Params); err != nil {
					run(scope, func() { srv.writeError(req.ID, ErrCodeInvalidParams, err.Error(), scope) })
					continue
				}
				terminal, terminalReady, terminalScope = req, ready, scope
				break loop
			}
			if req.Method != MethodInit && !ready {
				run(scope, func() { srv.writeError(req.ID, ErrCodeInvalidRequest, "successful init required", scope) })
				continue
			}
			if req.Method == MethodInit {
				if !req.ID.positiveInteger() {
					run(scope, func() {
						srv.writeError(req.ID, ErrCodeInvalidRequest, "init requires a positive safe request ID", scope)
					})
					continue
				}
				if initUsed {
					run(scope, func() { srv.writeError(req.ID, ErrCodeInvalidRequest, "init already attempted", scope) })
					continue
				}
				initUsed = true
				if strictjson.Validate(line) != nil {
					srv.initMu.Lock()
					srv.initAttempted = true
					srv.initMu.Unlock()
					run(scope, func() { srv.writeInitError(req.ID, initInvalid("request"), scope) })
					continue
				}
				dispatch(*req, scope)
			} else {
				dispatch(*req, scope)
			}
		}
	}
	// Explicit unload keeps demux alive for pending replies through drain/cleanup.
	if terminal != nil {
		go func() {
			for {
				select {
				case event := <-events:
					line := event.raw
					req, fault := decodeEnvelope(line)
					if req != nil && fault == nil && req.Method == "rpc/cancel" && req.ID == (RPCID{}) {
						raw, err := paramsJSON(req.Params)
						if err == nil && ValidateRPCControlDTO("CancelParams", raw, core.directional) == nil {
							var p CancelParams
							_ = json.Unmarshal(raw, &p)
							admissions.cancelRequest(p)
						}
					}
					if core.directional && fault != nil && (!json.Valid(line) || replyCandidate(line)) {
						srv.fence(errCorrelation)
						core.close(errCorrelation)
						return
					}
					if req == nil && fault == nil && core.directional {
						if err := core.reply(line); err != nil {
							srv.fence(err)
							core.close(err)
							return
						}
					}
				case <-readerDone:
					if readerErr != nil {
						srv.fence(readerErr)
					}
					core.close(errConnectionClosed)
					return
				case <-stopRead:
					return
				}
			}
		}()
	} else {
		core.close(inputErr)
	}
	cancel() // Fence is already closed; admitted callbacks receive cancellation.
	if ownInput && terminal == nil {
		_ = os.Stdin.Close()
	}
	defer func() {
		if ownInput {
			_ = os.Stdin.Close()
		}
	}()
	shutdownBudget := timeout
	if terminalScope != nil {
		if end, ok := terminalScope.ctx.Deadline(); ok && time.Until(end) < shutdownBudget {
			shutdownBudget = time.Until(end)
		}
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownBudget)
	if terminalScope != nil {
		stop := context.AfterFunc(terminalScope.ctx, func() {
			cause := context.Cause(terminalScope.ctx)
			var cancelled *TransportCancelledError
			var expired *DeadlineExceededError
			if errors.As(cause, &cancelled) || errors.As(cause, &expired) || errors.Is(cause, context.DeadlineExceeded) {
				shutdownCancel()
			}
		})
		defer stop()
	}
	defer shutdownCancel()
	drained := make(chan struct{})
	go func() { pending.Wait(); close(drained) }()
	fail := func() error {
		if ownOutput {
			_ = os.Stdout.Close()
		}
		return ErrShutdownTimeout
	}
	select {
	case <-drained:
	case <-shutdownCtx.Done():
		return fail()
	}
	if terminal != nil && terminalReady {
		forward, _ := validateRuntimeParams(terminal.Method, terminal.Params)
		shutdownCtx = withForwardContext(shutdownCtx, forward)
	}
	if shutdownCtx.Err() != nil {
		return fail()
	}
	if terminalScope != nil {
		if !terminalScope.start() {
			return fail()
		}
		shutdownCtx = context.WithValue(shutdownCtx, requestScopeKey{}, terminalScope)
	}
	cleanup := srv.beginUnload(shutdownCtx)
	select {
	case <-cleanup:
	case <-shutdownCtx.Done():
		return fail()
	}
	if terminal != nil {
		// Enqueue the only terminal response after drain and the one cleanup attempt.
		done := run(terminalScope, func() {
			if !terminalReady {
				srv.writeError(terminal.ID, ErrCodeInvalidRequest, "successful init required", terminalScope)
			} else if srv.unloadErr != nil {
				srv.writeErrorFromPluginErr(terminal.ID, srv.unloadErr, terminalScope)
			} else {
				srv.writeResult(terminal.ID, map[string]bool{"ok": true}, terminalScope)
			}
		})
		select {
		case <-done:
		case <-shutdownCtx.Done():
			return fail()
		}
	}
	writer.seal() // All producers completed; writer flushes queued frames.
	select {
	case <-writerDone:
	case <-shutdownCtx.Done():
		return fail()
	}
	if err := writer.failure(); err != nil {
		return err
	}
	if srv.unloadErr != nil && terminal == nil {
		return srv.unloadErr
	}
	select {
	case failure := <-fatal:
		if inputErr == nil {
			inputErr = failure
		}
	default:
	}
	return inputErr
}

func (s *server) beginUnload(ctx context.Context) <-chan struct{} {
	s.unloadOnce.Do(func() {
		go func() {
			defer close(s.unloadDone)
			defer func() {
				if r := recover(); r != nil {
					s.unloadErr = fmt.Errorf("panic: %v", r)
				}
			}()
			s.unloadErr = s.plugin.Unload(ctx)
		}()
	})
	return s.unloadDone
}
func (s *server) unload(ctx context.Context) error {
	select {
	case <-s.beginUnload(ctx):
		return s.unloadErr
	case <-ctx.Done():
		return ErrShutdownTimeout
	}
}
