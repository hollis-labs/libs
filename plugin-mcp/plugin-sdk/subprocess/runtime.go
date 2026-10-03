package subprocess

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

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
}

// ServeWithOptions shares one shutdown path for unload, EOF and cancellation.
// Only runtime-owned stdin/stdout may be closed to interrupt pending I/O.
func ServeWithOptions(p Plugin, options ServeOptions) error {
	if p == nil {
		return errors.New("subprocess: Serve called with nil plugin")
	}
	timeout := options.ShutdownTimeout
	if timeout == 0 {
		timeout = DefaultShutdownTimeout
	}
	if timeout < 0 {
		return errors.New("subprocess: shutdown timeout must be positive")
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
	srv := &server{plugin: p, logger: logger, secrets: secrets, unloadDone: make(chan struct{})}
	srv.detectCapabilities()

	// A single writer owns the transport. Producers stop enqueueing after Serve
	// returns, including callbacks that ignored cancellation and finished late.
	finished := make(chan struct{})
	defer close(finished)
	messages := make(chan []byte, 32)
	writerDone := make(chan struct{})
	var writerErr error
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-finished:
				return
			default:
			}
			select {
			case <-finished:
				return
			case data, ok := <-messages:
				if !ok {
					return
				}
				n, err := out.Write(data)
				if err == nil && n != len(data) {
					err = io.ErrShortWrite
				}
				if err != nil {
					writerErr = fmt.Errorf("stdout: %w", err)
					return
				}
			}
		}
	}()
	srv.writeFrame = func(data []byte) {
		select {
		case <-finished:
			return
		case <-writerDone:
			return
		default:
		}
		select {
		case messages <- data:
		case <-finished:
		case <-writerDone:
		}
	}
	stopRead := make(chan struct{})
	defer close(stopRead)
	events := make(chan []byte)
	readerDone := make(chan struct{})
	var readerErr error
	go func() {
		defer close(readerDone)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			line := append([]byte{}, scanner.Bytes()...)
			select {
			case events <- line:
			case <-stopRead:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			readerErr = fmt.Errorf("stdin scanner: %w", err)
		}
	}()
	var pending sync.WaitGroup
	run := func(call func()) <-chan struct{} {
		done := make(chan struct{})
		pending.Add(1)
		go func() { defer pending.Done(); defer close(done); call() }()
		return done
	}
	dispatch := func(req RPCRequest) <-chan struct{} {
		return run(func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("subprocess: panic in handler", "method", req.Method, "stack", string(debug.Stack()))
					srv.writeError(req.ID, ErrCodeInternal, fmt.Sprintf("panic: %v", r))
				}
			}()
			srv.dispatch(ctx, req)
		})
	}
	var initDone <-chan struct{}
	var terminal *RPCRequest
	terminalReady := false
	var inputErr error
loop:
	for {
		input := events
		if initDone != nil {
			input = nil
		}
		select {
		case <-ctx.Done():
			break loop
		case <-writerDone:
			break loop
		case <-readerDone:
			inputErr = readerErr
			break loop
		case <-initDone:
			initDone = nil
		case line := <-input:
			req, fault := decodeEnvelope(line)
			if fault != nil {
				run(func() { srv.writeMessage(*fault) })
				continue
			}
			if req == nil {
				continue
			}
			srv.initMu.Lock()
			ready, attempted := srv.initialized, srv.initAttempted
			srv.initMu.Unlock()
			if req.Method == MethodUnload {
				if _, err := validateRuntimeParams(req.Method, req.Params); err != nil {
					run(func() { srv.writeError(req.ID, ErrCodeInvalidParams, err.Error()) })
					continue
				}
				terminal, terminalReady = req, ready
				break loop
			}
			if req.Method != MethodInit && !ready {
				run(func() { srv.writeError(req.ID, ErrCodeInvalidRequest, "successful init required") })
				continue
			}
			if req.Method == MethodInit {
				if !req.ID.positiveInteger() {
					run(func() { srv.writeError(req.ID, ErrCodeInvalidRequest, "init requires a positive safe request ID") })
					continue
				}
				if attempted {
					run(func() { srv.writeError(req.ID, ErrCodeInvalidRequest, "init already attempted") })
					continue
				}
				if strictjson.Validate(line) != nil {
					srv.initMu.Lock()
					srv.initAttempted = true
					srv.initMu.Unlock()
					run(func() { srv.writeInitError(req.ID, initInvalid("request")) })
					continue
				}
				initDone = dispatch(*req)
			} else {
				dispatch(*req)
			}
		}
	}
	cancel() // Fence is already closed; admitted callbacks receive cancellation.
	if ownInput {
		_ = os.Stdin.Close()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), timeout)
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
	cleanup := srv.beginUnload(shutdownCtx)
	select {
	case <-cleanup:
	case <-shutdownCtx.Done():
		return fail()
	}
	if terminal != nil {
		// Enqueue the only terminal response after drain and the one cleanup attempt.
		done := run(func() {
			if !terminalReady {
				srv.writeError(terminal.ID, ErrCodeInvalidRequest, "successful init required")
			} else if srv.unloadErr != nil {
				srv.writeErrorFromPluginErr(terminal.ID, srv.unloadErr)
			} else {
				srv.writeResult(terminal.ID, map[string]bool{"ok": true})
			}
		})
		select {
		case <-done:
		case <-shutdownCtx.Done():
			return fail()
		}
	}
	close(messages) // All producers completed; writer flushes queued frames.
	select {
	case <-writerDone:
	case <-shutdownCtx.Done():
		return fail()
	}
	if writerErr != nil {
		return writerErr
	}
	if srv.unloadErr != nil && terminal == nil {
		return srv.unloadErr
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
