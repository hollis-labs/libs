//go:build !windows

package agentsessions

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
)

type rpcTestWriter struct{ frames chan []byte }

func (w *rpcTestWriter) Write(b []byte) (int, error) {
	w.frames <- append([]byte(nil), b...)
	return len(b), nil
}
func (w *rpcTestWriter) Close() error { return nil }
func receiveRPC[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("structured transport stalled")
		var zero T
		return zero
	}
}
func newAsyncTestSession(t *testing.T, responder func(context.Context, JsonRpcServerRequest) (any, *JsonRpcError)) (*jsonRpcStdioSession, *jsonRpcServerAttempt, *rpcTestWriter) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "rpc-log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	w := &rpcTestWriter{frames: make(chan []byte, 128)}
	s := &jsonRpcStdioSession{stdin: w, logFile: f, adapter: &minimalAdapter{}, pending: make(map[int64]chan jsonRpcResponse), requestContext: context.Background()}
	s.opts.JsonRpcRequestResponder = responder
	a := &jsonRpcServerAttempt{binding: gatetest.Request(time.Now()).Binding, pending: make(map[string]*jsonRpcServerPending)}
	s.serverAttempt = a
	t.Cleanup(func() { s.cancelServerAttempt(s.serverAttempt) })
	return s, a, w
}

func TestAsyncRequestReaderContinuesAndResolvedCancels(t *testing.T) {
	entered := make(chan JsonRpcServerRequest, 1)
	cancelled := make(chan struct{})
	notified := make(chan string, 4)
	s, a, w := newAsyncTestSession(t, func(ctx context.Context, r JsonRpcServerRequest) (any, *JsonRpcError) {
		entered <- r
		<-ctx.Done()
		close(cancelled)
		return map[string]string{"decision": "accept"}, nil
	})
	s.opts.JsonRpcNotificationHook = func(method string, _ json.RawMessage) { notified <- method }
	response := make(chan jsonRpcResponse, 1)
	s.pending[42] = response
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() { s.runReaderLoop(reader, a); close(done) }()
	_, _ = io.WriteString(writer, "{\"id\":9007199254740993,\"method\":\"item/commandExecution/requestApproval\",\"params\":{\"threadId\":\"thread\",\"turnId\":\"turn\"}}\n")
	r := receiveRPC(t, entered)
	if string(r.ID) != "9007199254740993" || r.Binding != a.binding {
		t.Fatalf("lost request binding: %+v", r)
	}
	_, _ = io.WriteString(writer, "{\"id\":42,\"result\":{\"ok\":true}}\n{\"method\":\"serverRequest/resolved\",\"params\":{\"threadId\":\"thread\",\"requestId\":9007199254740993}}\n")
	if string(receiveRPC(t, response).result) != `{"ok":true}` {
		t.Fatal("reader lost outbound response")
	}
	if receiveRPC(t, notified) != "serverRequest/resolved" {
		t.Fatal("reader lost notification")
	}
	receiveRPC(t, cancelled)
	_ = writer.Close()
	receiveRPC(t, done)
	select {
	case b := <-w.frames:
		t.Fatalf("cleared request received late reply: %s", b)
	default:
	}
}

func TestAsyncRequestReplacementFencesLateReplyAndKeepsIDType(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	oldReturned := make(chan struct{})
	s, a, w := newAsyncTestSession(t, func(_ context.Context, r JsonRpcServerRequest) (any, *JsonRpcError) {
		if r.Binding.Generation == 7 {
			close(entered)
			<-release
			defer close(oldReturned)
		}
		// A responder mutating its detached envelope cannot change reply identity.
		r.ID[0] = '0'
		return map[string]string{"decision": "accept"}, nil
	})
	id := json.RawMessage(`9007199254740993`)
	s.respondToServerRequestAsync(a, jsonRpcFrame{ID: &id, Method: "approval"})
	receiveRPC(t, entered)
	b := &jsonRpcServerAttempt{binding: a.binding, pending: make(map[string]*jsonRpcServerPending)}
	b.binding.Generation++
	b.binding.AttemptID = "attempt-8"
	s.ioLock.Lock()
	s.replaceServerAttemptLocked(b)
	s.ioLock.Unlock()
	close(release)
	receiveRPC(t, oldReturned)
	// Same numeric ID in the successor belongs only to the successor callback.
	s.respondToServerRequestAsync(b, jsonRpcFrame{ID: &id, Method: "approval"})
	frame := receiveRPC(t, w.frames)
	var got struct {
		ID     json.RawMessage
		Result json.RawMessage
	}
	if json.Unmarshal(frame, &got) != nil || string(got.ID) != string(id) {
		t.Fatalf("numeric identity changed: %s", frame)
	}
	stringID := json.RawMessage(`"9007199254740993"`)
	s.respondToServerRequestAsync(b, jsonRpcFrame{ID: &stringID, Method: "approval"})
	frame = receiveRPC(t, w.frames)
	if json.Unmarshal(frame, &got) != nil || string(got.ID) != string(stringID) {
		t.Fatalf("string identity changed: %s", frame)
	}
	select {
	case extra := <-w.frames:
		t.Fatalf("old attempt wrote into successor: %s", extra)
	default:
	}
}

func TestAsyncRequestLifecycleCancelsBlockedResponder(t *testing.T) {
	for _, method := range []string{"$/cancelRequest", "turn/completed", "turn/started", "thread/closed", "eof", "stop"} {
		t.Run(method, func(t *testing.T) {
			entered := make(chan struct{})
			cancelled := make(chan struct{})
			s, a, _ := newAsyncTestSession(t, func(ctx context.Context, _ JsonRpcServerRequest) (any, *JsonRpcError) {
				close(entered)
				<-ctx.Done()
				close(cancelled)
				return nil, nil
			})
			id := json.RawMessage(`"pending"`)
			s.respondToServerRequestAsync(a, jsonRpcFrame{ID: &id, Method: "approval", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn"}`)})
			receiveRPC(t, entered)
			switch method {
			case "eof":
				s.runReaderLoop(&emptyReader{}, a)
			case "stop":
				s.stopRequested = make(chan struct{})
				s.alive.Store(true)
				if err := s.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
			default:
				params := json.RawMessage(`{"threadId":"thread","turn":{"id":"turn"},"id":"pending"}`)
				if method == "turn/started" {
					params = json.RawMessage(`{"threadId":"thread","turn":{"id":"new-turn"}}`)
				}
				s.cancelServerRequests(a, method, params)
			}
			receiveRPC(t, cancelled)
		})
	}
}

// A transport that emits no approval cannot be treated as enforcement coverage.
// This is a negative library control; the registry's posture tests separately
// cover on-request/never mappings, without launching an installed provider.
func TestAsyncRequestNoEmittedApprovalMakesNoDecision(t *testing.T) {
	called := make(chan struct{}, 1)
	s, a, w := newAsyncTestSession(t, func(context.Context, JsonRpcServerRequest) (any, *JsonRpcError) {
		called <- struct{}{}
		return map[string]string{"decision": "accept"}, nil
	})
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() { s.runReaderLoop(reader, a); close(done) }()
	_, _ = io.WriteString(writer, "{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread\",\"turn\":{\"id\":\"turn\",\"status\":\"completed\"}}}\n")
	_ = writer.Close()
	receiveRPC(t, done)
	select {
	case <-called:
		t.Fatal("absence of callback manufactured a decision")
	default:
	}
	select {
	case <-w.frames:
		t.Fatal("absence of callback manufactured an approval reply")
	default:
	}
}

type emptyReader struct{}

func (*emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

type blockedRPCWriter struct {
	entered, closed chan struct{}
	once            sync.Once
}

func (w *blockedRPCWriter) Write([]byte) (int, error) {
	close(w.entered)
	<-w.closed
	return 0, io.ErrClosedPipe
}
func (w *blockedRPCWriter) Close() error { w.once.Do(func() { close(w.closed) }); return nil }

func TestAsyncBlockedWriteDoesNotPreventStopOrCancellation(t *testing.T) {
	for _, cause := range []string{"stop", "resolved", "eof"} {
		t.Run(cause, func(t *testing.T) {
			s, a, _ := newAsyncTestSession(t, func(context.Context, JsonRpcServerRequest) (any, *JsonRpcError) {
				return map[string]string{"decision": "accept"}, nil
			})
			w := &blockedRPCWriter{entered: make(chan struct{}), closed: make(chan struct{})}
			s.stdin = w
			observed := make(chan JsonRpcReplyStatus, 1)
			s.opts.JsonRpcReplyObserver = func(_ JsonRpcServerRequest, status JsonRpcReplyStatus, err error) {
				if err == nil {
					t.Error("failed write lost error")
				}
				observed <- status
			}
			id := json.RawMessage(`"blocked"`)
			s.respondToServerRequestAsync(a, jsonRpcFrame{ID: &id, Method: "approval", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn"}`)})
			receiveRPC(t, w.entered)
			done := make(chan struct{})
			go func() {
				switch cause {
				case "stop":
					s.stopRequested = make(chan struct{})
					_ = s.Stop(context.Background())
				case "resolved":
					s.cancelServerRequests(a, "serverRequest/resolved", json.RawMessage(`{"threadId":"thread","requestId":"blocked"}`))
				case "eof":
					s.runReaderLoop(&emptyReader{}, a)
				}
				close(done)
			}()
			receiveRPC(t, done)
			if receiveRPC(t, observed) != JsonRpcReplyUnknown {
				t.Fatal("failed write reported delivered")
			}
		})
	}
}
