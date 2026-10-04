package subprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const duplexLogParams = `{"grant_id":"g","context":{"binding_id":"b","timeout_ms":1,"parent_call":{"request_owner":"host","id":1}},"level":"info","message":"ready"}`

type duplexRig struct {
	send  func(string)
	read  func() map[string]json.RawMessage
	input *io.PipeWriter
	done  <-chan error
}

func newDuplexRig(t *testing.T, p Plugin, c *correlation) duplexRig {
	t.Helper()
	in, feed := io.Pipe()
	out, writer := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(func() { cancel(); in.Close(); feed.Close(); out.Close(); writer.Close() })
	done := make(chan error, 1)
	go func() {
		err := serveConnection(p, ServeOptions{Input: in, Output: writer, Context: ctx}, c)
		writer.Close()
		done <- err
	}()
	decoder := json.NewDecoder(out)
	return duplexRig{input: feed, done: done, send: func(raw string) {
		t.Helper()
		if _, err := io.WriteString(feed, strings.TrimRight(raw, "\n")+"\n"); err != nil {
			t.Fatal(err)
		}
	}, read: func() map[string]json.RawMessage {
		t.Helper()
		var fields map[string]json.RawMessage
		if err := decoder.Decode(&fields); err != nil {
			t.Fatal(err)
		}
		return fields
	}}
}
func TestSharedDuplexRuntimeRecipe(t *testing.T) {
	raw, err := os.ReadFile("../protocol/v2/fixtures/duplex-correlation.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Runtime []struct {
			Name        string
			Directional bool
			Steps       []struct {
				Op                    string
				Send, Expect, Effects json.RawMessage
			}
		}
	}
	if json.Unmarshal(raw, &corpus) != nil {
		t.Fatal("runtime corpus")
	}
	for _, v := range corpus.Runtime {
		t.Run(v.Name, func(t *testing.T) {
			c := newCorrelation(v.Directional)
			p := &shutdownPlugin{}
			var initialized atomic.Bool
			var healths, unloads atomic.Int32
			call := func() error {
				ch, err := c.call("host/log", json.RawMessage(duplexLogParams))
				if err != nil {
					return err
				}
				reply := <-ch
				if string(reply.result) != `{"accepted":true}` {
					return errors.New("missing host result")
				}
				return reply.err
			}
			p.initFn = func(ctx context.Context, v InitParams) (InitResult, error) {
				if err := call(); err != nil {
					return InitResult{}, err
				}
				initialized.Store(true)
				return p.basePlugin.Init(ctx, v)
			}
			p.unloadFn = func(context.Context) error { unloads.Add(1); return call() }
			p.healthFn = func(context.Context) (HealthStatus, error) { healths.Add(1); return HealthStatus{OK: true}, nil }
			r := newDuplexRig(t, p, c)
			for _, step := range v.Steps {
				switch step.Op {
				case "send_host", "send_host_response":
					var frame bytes.Buffer
					if err := json.Compact(&frame, step.Send); err != nil {
						t.Fatal(err)
					}
					r.send(frame.String())
				case "expect_plugin_request", "expect_plugin_response":
					actual := r.read()
					actualRaw, _ := json.Marshal(actual)
					assertJSONSubset(t, actualRaw, step.Expect)
					if body := actual["result"]; body != nil {
						var fields map[string]json.RawMessage
						_ = json.Unmarshal(body, &fields)
						if _, ok := fields["reverse_rpc_version"]; ok {
							t.Fatal("reverse ack advertised")
						}
					}
				case "expect_exit":
					if err := <-r.done; err != nil {
						t.Fatal(err)
					}
					fallthrough
				case "barrier":
					effects, _ := json.Marshal(map[string]any{"initialized": initialized.Load(), "health_calls": healths.Load(), "unload_attempts": unloads.Load()})
					assertJSONSubset(t, effects, step.Effects)
				default:
					t.Fatal(step.Op)
				}
			}
		})
	}
}
func assertJSONSubset(t *testing.T, actualRaw, expectedRaw []byte) {
	t.Helper()
	var actual, expected any
	if json.Unmarshal(actualRaw, &actual) != nil || json.Unmarshal(expectedRaw, &expected) != nil {
		t.Fatal("invalid expected/actual JSON")
	}
	var compare func(any, any) bool
	compare = func(a, e any) bool {
		if fields, ok := e.(map[string]any); ok {
			values, ok := a.(map[string]any)
			if !ok {
				return false
			}
			for key, value := range fields {
				if !compare(values[key], value) {
					return false
				}
			}
			return true
		}
		return reflect.DeepEqual(a, e)
	}
	if !compare(actual, expected) {
		t.Fatalf("actual=%s expected subset=%s", actualRaw, expectedRaw)
	}
}
func TestDuplexEOFCompletesPending(t *testing.T) {
	c := newCorrelation(true)
	completed := make(chan error, 1)
	p := &shutdownPlugin{initFn: func(context.Context, InitParams) (InitResult, error) {
		ch, err := c.call("host/log", json.RawMessage(duplexLogParams))
		if err == nil {
			err = (<-ch).err
		}
		completed <- err
		return InitResult{}, err
	}}
	r := newDuplexRig(t, p, c)
	r.send(initLine(t, 1))
	r.read()
	r.input.Close()
	if err := <-completed; !errors.Is(err, errConnectionClosed) {
		t.Fatal(err)
	}
	r.read()
	if err := <-r.done; err != nil {
		t.Fatal(err)
	}
}
func TestBaseDuplicateLiveRequestFences(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	p := &shutdownPlugin{commandFn: func(ctx context.Context, _ CommandRequest) (CommandResult, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return CommandResult{}, ctx.Err()
	}}
	r := newDuplexRig(t, p, newCorrelation(false))
	r.send(initLine(t, 8000))
	r.read()
	request := `{"jsonrpc":"2.0","id":"live","method":"command/execute","params":{"name":"hold","args":"","session_id":""}}`
	r.send(request)
	<-started
	r.send(request)
	// Read the first callback's terminal failure so a pipe write can complete.
	r.read()
	if err := <-r.done; !errors.Is(err, errCorrelation) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate callback")
	}
}

type receiptTestWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w receiptTestWriter) Write(b []byte) (int, error) {
	close(w.started)
	<-w.release
	return len(b), nil
}
func TestPublicationReceiptOwnsInboundLifetime(t *testing.T) {
	output := receiptTestWriter{make(chan struct{}), make(chan struct{})}
	w := newFrameWriter(output, time.Second, func() {})
	defer w.abort(errConnectionClosed)
	c := newCorrelation(false)
	if err := c.admit(StringID("held")); err != nil {
		t.Fatal(err)
	}
	receipt := make(chan error, 1)
	if err := w.submit([]byte("{}\n"), func(err error) { c.release(StringID("held")); receipt <- err }); err != nil {
		t.Fatal(err)
	}
	<-output.started
	if err := c.admit(StringID("held")); !errors.Is(err, errCorrelation) {
		t.Fatal("released before whole write")
	}
	close(output.release)
	if err := <-receipt; err != nil {
		t.Fatal(err)
	}
	if err := c.admit(StringID("held")); err != nil {
		t.Fatal(err)
	}
	w.seal()
	<-w.done
}

func TestUnloadKeepsAdmissionSnapshotWhileInitFinishes(t *testing.T) {
	started := make(chan struct{})
	p := &shutdownPlugin{}
	var unloads atomic.Int32
	p.initFn = func(ctx context.Context, v InitParams) (InitResult, error) {
		close(started)
		<-ctx.Done()
		return p.basePlugin.Init(ctx, v)
	}
	p.unloadFn = func(context.Context) error { unloads.Add(1); return nil }
	r := newDuplexRig(t, p, newCorrelation(false))
	r.send(initLine(t, 1))
	<-started
	r.send(`{"jsonrpc":"2.0","id":2,"method":"plugin/unload"}`)
	r.read()
	terminal := r.read()
	var failure RPCError
	if json.Unmarshal(terminal["error"], &failure) != nil || failure.Code != -32600 {
		t.Fatal(terminal)
	}
	if err := <-r.done; err != nil {
		t.Fatal(err)
	}
	if unloads.Load() != 1 {
		t.Fatal("cleanup repeated")
	}
}
