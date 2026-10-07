package subprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	plugin "github.com/hollis-labs/libs/plugin-mcp/plugin-sdk"
)

type shutdownPlugin struct {
	basePlugin
	initFn    func(context.Context, InitParams) (InitResult, error)
	loadFn    func(context.Context) (LoadResult, error)
	unloadFn  func(context.Context) error
	healthFn  func(context.Context) (HealthStatus, error)
	commandFn func(context.Context, CommandRequest) (CommandResult, error)
}

func (p *shutdownPlugin) Init(ctx context.Context, v InitParams) (InitResult, error) {
	if p.initFn != nil {
		return p.initFn(ctx, v)
	}
	return p.basePlugin.Init(ctx, v)
}
func (p *shutdownPlugin) Load(ctx context.Context) (LoadResult, error) {
	if p.loadFn != nil {
		return p.loadFn(ctx)
	}
	return p.basePlugin.Load(ctx)
}
func (p *shutdownPlugin) Unload(ctx context.Context) error {
	if p.unloadFn != nil {
		return p.unloadFn(ctx)
	}
	return nil
}
func (p *shutdownPlugin) Health(ctx context.Context) (HealthStatus, error) {
	if p.healthFn != nil {
		return p.healthFn(ctx)
	}
	return HealthStatus{OK: true}, nil
}
func (p *shutdownPlugin) Command(ctx context.Context, v CommandRequest) (CommandResult, error) {
	if p.commandFn != nil {
		return p.commandFn(ctx, v)
	}
	return CommandResult{Action: "noop"}, nil
}

func TestLifecycleTypedErrors(t *testing.T) {
	for _, method := range []string{MethodInit, MethodLoad, MethodHealth, MethodUnload} {
		for _, failure := range []struct {
			err  error
			code int
		}{
			{fmt.Errorf("wrapped: %w", plugin.ErrNotFound("missing")), ErrCodeNotFound},
			{plugin.ErrConflict("conflict"), ErrCodeConflict},
			{plugin.ErrValidation("invalid"), ErrCodeValidation},
			{fmt.Errorf("wrapped: %w", plugin.ErrCancelled), ErrCodeCancelled},
			{errors.New("failure"), ErrCodeInternal},
		} {
			t.Run(fmt.Sprintf("%s/%d", method, failure.code), func(t *testing.T) {
				p := &shutdownPlugin{}
				switch method {
				case MethodInit:
					p.initFn = func(context.Context, InitParams) (InitResult, error) { return InitResult{}, failure.err }
				case MethodLoad:
					p.loadFn = func(context.Context) (LoadResult, error) { return LoadResult{}, failure.err }
				case MethodHealth:
					p.healthFn = func(context.Context) (HealthStatus, error) { return HealthStatus{}, failure.err }
				case MethodUnload:
					p.unloadFn = func(context.Context) error { return failure.err }
				}
				req := RPCRequest{JSONRPC: "2.0", ID: NumberID(1), Method: method}
				if method == MethodInit {
					req.Params = validInitParams()
				}
				got := drive(t, p, []RPCRequest{req})
				if len(got) != 1 || got[0].Error == nil || got[0].Error.Code != failure.code {
					t.Fatalf("responses=%+v", got)
				}
			})
		}
	}
}
func TestHealthIntentionalUnhealthyAndPanic(t *testing.T) {
	p := &shutdownPlugin{healthFn: func(context.Context) (HealthStatus, error) {
		return HealthStatus{OK: false, Message: "maintenance"}, nil
	}}
	got := drive(t, p, []RPCRequest{{JSONRPC: "2.0", ID: NumberID(1), Method: MethodHealth}})
	var result HealthResult
	if got[0].Error != nil || json.Unmarshal(got[0].Result, &result) != nil || result.OK || result.Message != "maintenance" {
		t.Fatalf("response=%+v", got[0])
	}
	p.healthFn = func(context.Context) (HealthStatus, error) { panic("health panic") }
	got = drive(t, p, []RPCRequest{{JSONRPC: "2.0", ID: NumberID(1), Method: MethodHealth}})
	if got[0].Error == nil || got[0].Error.Code != ErrCodeInternal {
		t.Fatalf("response=%+v", got[0])
	}
}

func TestUnloadTerminalAndOnceWithoutHostEOF(t *testing.T) {
	var unloads, healths atomic.Int32
	p := &shutdownPlugin{unloadFn: func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Error("cleanup did not get fresh context")
		}
		unloads.Add(1)
		return nil
	}, healthFn: func(context.Context) (HealthStatus, error) { healths.Add(1); return HealthStatus{OK: true}, nil }}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	t.Cleanup(func() { inR.Close(); inW.Close(); outR.Close(); outW.Close() })
	done := make(chan error, 1)
	go func() { done <- serveWith(p, inR, outW) }()
	init := initLine(t, 1)
	written := make(chan error, 1)
	go func() { _, err := io.WriteString(inW, init); written <- err }()
	var initReply RPCResponse
	decoder := json.NewDecoder(outR)
	if err := decoder.Decode(&initReply); err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := io.WriteString(inW, `{"jsonrpc":"2.0","id":"end","method":"plugin/unload"}`+"\n"+`{"jsonrpc":"2.0","id":9,"method":"plugin/health"}`+"\n")
		written <- err
	}()
	var reply RPCResponse
	if err := decoder.Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.ID != StringID("end") || reply.Error != nil {
		t.Fatalf("terminal=%+v", reply)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve waited for host EOF")
	}
	if unloads.Load() != 1 || healths.Load() != 0 {
		t.Fatalf("unloads=%d healths=%d", unloads.Load(), healths.Load())
	}
	// Caller owns the injected pipe and can still write/use it until it closes it.
	inR.Close()
	if err := <-written; err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

func TestShutdownCancelsDrainsThenCleansOnce(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			finished := make(chan struct{})
			var unloads atomic.Int32
			p := &shutdownPlugin{commandFn: func(ctx context.Context, _ CommandRequest) (CommandResult, error) {
				close(started)
				<-ctx.Done()
				close(finished)
				return CommandResult{}, ctx.Err()
			}, unloadFn: func(ctx context.Context) error {
				select {
				case <-finished:
				default:
					t.Error("cleanup before admitted callback finished")
				}
				if ctx.Err() != nil {
					t.Error("cleanup already cancelled")
				}
				unloads.Add(1)
				return nil
			}}
			inR, inW := io.Pipe()
			defer inR.Close()
			defer inW.Close()
			var out bytes.Buffer
			done := make(chan error, 1)
			ack := make(chan RPCID, 1)
			var mu sync.Mutex
			writer := &syncWriter{buf: &out, mu: &mu, published: ack}
			go func() { done <- ServeWithOptions(p, ServeOptions{Input: inR, Output: writer, Context: parent}) }()
			io.WriteString(inW, initLine(t, 1))
			<-ack
			io.WriteString(inW, `{"jsonrpc":"2.0","id":2,"method":"command/execute","params":{"name":"test","args":"","session_id":""}}`+"\n")
			<-started
			if explicit {
				io.WriteString(inW, `{"jsonrpc":"2.0","id":3,"method":"plugin/unload"}`+"\n")
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("shutdown did not unblock idle reader")
			}
			if unloads.Load() != 1 {
				t.Fatalf("attempts=%d", unloads.Load())
			}
		})
	}
}

func TestUnloadFailureAndPanicAreNotRetried(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(fmt.Sprint(panics), func(t *testing.T) {
			for _, explicit := range []bool{false, true} {
				var calls atomic.Int32
				p := &shutdownPlugin{unloadFn: func(context.Context) error {
					calls.Add(1)
					if panics {
						panic("cleanup panic")
					}
					return plugin.ErrConflict("cleanup failed")
				}}
				var out bytes.Buffer
				reader, feed := io.Pipe()
				defer reader.Close()
				ack := make(chan RPCID, 1)
				var mu sync.Mutex
				writer := &syncWriter{buf: &out, mu: &mu, published: ack}
				go func() {
					defer feed.Close()
					io.WriteString(feed, initLine(t, 1))
					<-ack
					if explicit {
						io.WriteString(feed, `{"jsonrpc":"2.0","id":2,"method":"plugin/unload"}`+"\n")
					}
				}()
				err := serveWith(p, reader, writer)
				if calls.Load() != 1 {
					t.Fatalf("cleanup attempts=%d", calls.Load())
				}
				if !explicit && err == nil {
					t.Fatal("EOF cleanup failure disappeared")
				}
				if explicit {
					var reply RPCResponse
					dec := json.NewDecoder(&out)
					dec.Decode(&reply)
					dec.Decode(&reply)
					want := ErrCodeConflict
					if panics {
						want = ErrCodeInternal
					}
					if reply.Error == nil || reply.Error.Code != want {
						t.Fatalf("terminal=%+v", reply)
					}
				}
			}
		})
	}
}

func TestShutdownTimeoutDoesNotClaimCleanup(t *testing.T) {
	release := make(chan struct{})
	var attempts atomic.Int32
	p := &shutdownPlugin{commandFn: func(context.Context, CommandRequest) (CommandResult, error) {
		<-release
		return CommandResult{Action: "noop"}, nil
	}, unloadFn: func(context.Context) error { attempts.Add(1); return nil }}
	var out bytes.Buffer
	var mu sync.Mutex
	ack := make(chan RPCID, 1)
	writer := &syncWriter{buf: &out, mu: &mu, published: ack}
	input, feed := io.Pipe()
	defer input.Close()
	go func() {
		defer feed.Close()
		io.WriteString(feed, initLine(t, 1))
		<-ack
		io.WriteString(feed, `{"jsonrpc":"2.0","id":2,"method":"command/execute","params":{"name":"test","args":"","session_id":""}}`+"\n")
	}()
	err := ServeWithOptions(p, ServeOptions{Input: input, Output: writer, ShutdownTimeout: 20 * time.Millisecond})
	if !errors.Is(err, ErrShutdownTimeout) || attempts.Load() != 0 {
		t.Fatalf("error=%v attempts=%d", err, attempts.Load())
	}
	mu.Lock()
	before := out.String()
	mu.Unlock()
	close(release)
	// The late handler has no route to enqueue a successful response after return.
	if strings.Contains(before, `"action":"noop"`) {
		t.Fatal("unfinished handler reported success")
	}
}

func TestCleanupDeadlineAndBlockedWriter(t *testing.T) {
	t.Run("cleanup", func(t *testing.T) {
		release, exited := make(chan struct{}), make(chan struct{})
		var calls atomic.Int32
		p := &shutdownPlugin{unloadFn: func(ctx context.Context) error { calls.Add(1); <-ctx.Done(); <-release; close(exited); return nil }}
		err := ServeWithOptions(p, ServeOptions{Input: strings.NewReader(initLine(t, 1)), Output: io.Discard, ShutdownTimeout: 20 * time.Millisecond})
		if !errors.Is(err, ErrShutdownTimeout) || calls.Load() != 1 {
			t.Fatalf("error=%v calls=%d", err, calls.Load())
		}
		close(release)
		<-exited
	})
	t.Run("writer", func(t *testing.T) {
		writer := &blockedShutdownWriter{release: make(chan struct{}), done: make(chan struct{})}
		err := ServeWithOptions(&shutdownPlugin{}, ServeOptions{Input: strings.NewReader(initLine(t, 1)), Output: writer, ShutdownTimeout: 20 * time.Millisecond})
		if !errors.Is(err, ErrShutdownTimeout) {
			t.Fatalf("error=%v", err)
		}
		close(writer.release)
		<-writer.done
	})
}

type blockedShutdownWriter struct{ release, done chan struct{} }

func (w *blockedShutdownWriter) Write(b []byte) (int, error) {
	<-w.release
	close(w.done)
	return len(b), nil
}

func TestEOFBoundsUncooperativeInit(t *testing.T) {
	release, started, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p := &shutdownPlugin{}
	p.initFn = func(ctx context.Context, v InitParams) (InitResult, error) {
		close(started)
		<-release
		defer close(exited)
		return p.basePlugin.Init(ctx, v)
	}
	done := make(chan error, 1)
	go func() {
		done <- ServeWithOptions(p, ServeOptions{Input: strings.NewReader(initLine(t, 1)), Output: io.Discard, ShutdownTimeout: 20 * time.Millisecond})
	}()
	<-started
	if err := <-done; !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("error=%v", err)
	}
	close(release)
	<-exited
}

type shutdownErrorWriter struct{ short bool }

func (w shutdownErrorWriter) Write([]byte) (int, error) {
	if w.short {
		return 0, nil
	}
	return 0, io.ErrClosedPipe
}
func TestWriterFailuresStillCleanOnce(t *testing.T) {
	for _, short := range []bool{false, true} {
		var calls atomic.Int32
		p := &shutdownPlugin{unloadFn: func(context.Context) error { calls.Add(1); return nil }}
		err := ServeWithOptions(p, ServeOptions{Input: strings.NewReader(initLine(t, 1)), Output: shutdownErrorWriter{short}})
		want := error(io.ErrClosedPipe)
		if short {
			want = io.ErrShortWrite
		}
		if !errors.Is(err, want) || calls.Load() != 1 {
			t.Fatalf("error=%v calls=%d", err, calls.Load())
		}
	}
}

func TestUnloadBeforeInitIsTerminalRefusal(t *testing.T) {
	p := &transcriptShutdown{}
	var out bytes.Buffer
	err := serveWith(p, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"plugin/unload"}`+"\n"+`{"jsonrpc":"2.0","id":2,"method":"plugin/health"}`+"\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	var reply RPCResponse
	if err := json.NewDecoder(&out).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error == nil || reply.Error.Code != ErrCodeInvalidRequest || p.unloadAttempts != 1 || p.healthCalls != 0 {
		t.Fatalf("reply=%+v effects=%v", reply, p.Effects())
	}
}
