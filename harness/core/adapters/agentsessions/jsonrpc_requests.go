//go:build !windows

package agentsessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

// All fields below are guarded by the session ioLock, also used to replace the
// writer. The pointer identity fences replies even when a provider reuses IDs.
type jsonRpcServerAttempt struct {
	binding runtimecontract.Binding
	pending map[string]*jsonRpcServerPending
	closed  bool
}
type jsonRpcServerPending struct {
	cancel       context.CancelFunc
	thread, turn string
}

func (s *jsonRpcStdioSession) prepareServerAttempt(attempt int) (*jsonRpcServerAttempt, error) {
	if s.opts.JsonRpcRequestResponder == nil {
		return nil, nil
	}
	if s.opts.JsonRpcAttemptBinding == nil {
		return nil, fmt.Errorf("agentsessions: async responder requires host attempt binding")
	}
	b, err := s.opts.JsonRpcAttemptBinding(attempt)
	if err != nil {
		return nil, err
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	s.ioLock.Lock()
	defer s.ioLock.Unlock()
	if old := s.serverAttempt; old != nil && (old.binding.AttemptID == b.AttemptID || old.binding.Generation >= b.Generation) {
		return nil, fmt.Errorf("agentsessions: replacement requires fresh attempt and increasing generation")
	}
	return &jsonRpcServerAttempt{binding: b, pending: make(map[string]*jsonRpcServerPending)}, nil
}
func (s *jsonRpcStdioSession) replaceServerAttemptLocked(a *jsonRpcServerAttempt) {
	s.cancelServerAttemptLocked(s.serverAttempt)
	s.serverAttempt = a
}
func (s *jsonRpcStdioSession) cancelServerAttemptLocked(a *jsonRpcServerAttempt) {
	if a == nil {
		return
	}
	a.closed = true
	for key, p := range a.pending {
		p.cancel()
		delete(a.pending, key)
	}
}
func (s *jsonRpcStdioSession) cancelServerAttempt(a *jsonRpcServerAttempt) {
	s.ioLock.Lock()
	s.cancelServerAttemptLocked(a)
	var stdin io.WriteCloser
	if a != nil && a == s.serverAttempt {
		stdin = s.stdin
	}
	s.ioLock.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
}

func (s *jsonRpcStdioSession) respondToServerRequestAsync(a *jsonRpcServerAttempt, frame jsonRpcFrame) {
	if frame.ID == nil || gate.NativeRequestID(*frame.ID).Validate() != nil {
		return
	}
	id := bytes.Clone(*frame.ID)
	key := string(bytes.TrimSpace(id))
	var route struct {
		Thread string `json:"threadId"`
		Turn   string `json:"turnId"`
	}
	_ = json.Unmarshal(frame.Params, &route)
	s.ioLock.Lock()
	if a == nil || a != s.serverAttempt || a.closed {
		s.ioLock.Unlock()
		return
	}
	// An overfull/ambiguous stream cannot safely accept another owner. Close
	// input instead of blocking the reader behind a refusal write or spawning an
	// unbounded queue of error responders.
	if a.pending[key] != nil || s.serverWorkers >= 64 {
		err := fmt.Errorf("agentsessions: async request capacity or duplicate ID")
		s.cancelServerAttemptLocked(a)
		stdin := s.stdin
		s.ioLock.Unlock()
		s.readerFault.set(err)
		if stdin != nil {
			_ = stdin.Close()
		}
		s.failPendingOnClose(err)
		return
	}
	parent := s.requestContext
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	p := &jsonRpcServerPending{cancel: cancel, thread: route.Thread, turn: route.Turn}
	a.pending[key] = p
	s.serverWorkers++
	request := JsonRpcServerRequest{ID: bytes.Clone(id), Method: frame.Method, Params: bytes.Clone(frame.Params), Binding: a.binding}
	s.ioLock.Unlock()
	go func() {
		defer cancel()
		defer func() { s.ioLock.Lock(); s.serverWorkers--; s.ioLock.Unlock() }()
		callbackRequest := request
		callbackRequest.ID = bytes.Clone(request.ID)
		callbackRequest.Params = bytes.Clone(request.Params)
		result, rpcErr := s.opts.JsonRpcRequestResponder(ctx, callbackRequest)
		// A prepared admission callback runs only after serialization. It holds
		// its host fence through the one-shot send, never just while queuing bytes.
		s.writeLock.Lock()
		status, err := s.deliverServerReply(ctx, a, p, key, id, result, rpcErr)
		s.ioLock.Lock()
		if a.pending[key] == p {
			delete(a.pending, key)
		}
		s.ioLock.Unlock()
		s.writeLock.Unlock()
		if status == JsonRpcReplySent {
			s.tickActivity()
		}
		s.observeServerReply(request, status, err)
	}()
}

// Caller holds writeLock, never ioLock, across this entire callback scope.
func (s *jsonRpcStdioSession) deliverServerReply(nativeCtx context.Context, a *jsonRpcServerAttempt, p *jsonRpcServerPending, key string, id json.RawMessage, result any, rpcErr *JsonRpcError) (JsonRpcReplyStatus, error) {
	s.ioLock.Lock()
	live := a == s.serverAttempt && !a.closed && a.pending[key] == p && nativeCtx.Err() == nil
	s.ioLock.Unlock()
	if !live {
		return JsonRpcReplyDiscarded, context.Canceled
	}
	var sendMu sync.Mutex
	active, used := true, false
	status := JsonRpcReplyDiscarded
	var writeErr error
	send := func(hostCtx context.Context, value any, replyErr *JsonRpcError) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		if !active || used {
			return fmt.Errorf("agentsessions: reply sender no longer available")
		}
		used = true
		if hostCtx == nil {
			writeErr = fmt.Errorf("agentsessions: reply context required")
			return writeErr
		}
		deliveryCtx, cancel := context.WithCancel(hostCtx)
		defer cancel()
		stopNative := context.AfterFunc(nativeCtx, cancel)
		defer stopNative()
		encoded, err := encodeServerReply(id, value, replyErr)
		if err != nil {
			writeErr = err
			return err
		}
		s.ioLock.Lock()
		live := a == s.serverAttempt && !a.closed && a.pending[key] == p && nativeCtx.Err() == nil && deliveryCtx.Err() == nil
		stdin := s.stdin
		s.ioLock.Unlock()
		if !live {
			writeErr = context.Canceled
			return writeErr
		}
		if stdin == nil {
			writeErr = ErrNoInputChannel
			return writeErr
		}
		// Deadline/revocation and lifecycle cancellation interrupt a blocked write
		// by closing only the captured originating writer, without lifecycle locks.
		stopClose := context.AfterFunc(deliveryCtx, func() { _ = stdin.Close() })
		defer stopClose()
		if err := deliveryCtx.Err(); err != nil {
			writeErr = err
			return err
		}
		if err := nativeCtx.Err(); err != nil {
			writeErr = err
			return err
		}
		status = JsonRpcReplyUnknown
		writeErr = writeRPCFrame(stdin, encoded)
		if writeErr == nil {
			writeErr = deliveryCtx.Err()
		}
		if writeErr == nil {
			writeErr = nativeCtx.Err()
		}
		if writeErr == nil {
			status = JsonRpcReplySent
		}
		return writeErr
	}
	var admissionErr error
	if prepared, ok := result.(*JsonRpcPreparedReply); ok && rpcErr == nil {
		if prepared == nil || prepared.Deliver == nil {
			admissionErr = fmt.Errorf("agentsessions: empty prepared reply")
		} else {
			admissionErr = prepared.Deliver(nativeCtx, send)
		}
	} else {
		admissionErr = send(nativeCtx, result, rpcErr)
	}
	sendMu.Lock()
	// A failed admission can only produce a refusal, never its prepared allow.
	refused := !used
	sendMu.Unlock()
	if refused {
		if admissionErr == nil {
			admissionErr = fmt.Errorf("agentsessions: prepared reply did not send")
		}
		_ = send(nativeCtx, nil, &JsonRpcError{Code: -32000, Message: "agentsessions: final reply admission refused"})
	}
	sendMu.Lock()
	active = false
	sendMu.Unlock()
	if writeErr != nil {
		return status, writeErr
	}
	return status, admissionErr
}

func (s *jsonRpcStdioSession) observeServerReply(r JsonRpcServerRequest, status JsonRpcReplyStatus, err error) {
	if observer := s.opts.JsonRpcReplyObserver; observer != nil {
		observer(r, status, err)
	}
}
func encodeServerReply(id json.RawMessage, result any, rpcErr *JsonRpcError) ([]byte, error) {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	return json.Marshal(resp)
}
func writeRPCFrame(w io.Writer, b []byte) error {
	b = append(b, '\n')
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		return io.ErrShortWrite
	}
	return err
}

func (s *jsonRpcStdioSession) cancelServerRequests(a *jsonRpcServerAttempt, method string, params json.RawMessage) {
	var n struct {
		RequestID json.RawMessage `json:"requestId"`
		ID        json.RawMessage `json:"id"`
		Thread    string          `json:"threadId"`
		TurnID    string          `json:"turnId"`
		Turn      struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(params, &n) != nil {
		return
	}
	s.ioLock.Lock()
	defer s.ioLock.Unlock()
	if a == nil || a != s.serverAttempt {
		return
	}
	for key, p := range a.pending {
		cancel := false
		switch method {
		case "serverRequest/resolved":
			cancel = n.Thread != "" && n.Thread == p.thread && gate.NativeRequestID(n.RequestID).Equal(gate.NativeRequestID(key))
		case "$/cancelRequest":
			cancel = gate.NativeRequestID(n.ID).Equal(gate.NativeRequestID(key))
		case "turn/completed":
			cancel = n.Thread != "" && n.Thread == p.thread && n.Turn.ID != "" && n.Turn.ID == p.turn
		case "turn/started":
			cancel = n.Thread != "" && n.Thread == p.thread && n.Turn.ID != "" && n.Turn.ID != p.turn
		case "thread/closed", "thread/archived":
			cancel = n.Thread != "" && n.Thread == p.thread
		}
		if cancel {
			p.cancel()
			delete(a.pending, key)
		}
	}
}
