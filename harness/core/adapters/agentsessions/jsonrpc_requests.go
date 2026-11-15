//go:build !windows

package agentsessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

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
		// A queued writer must recheck ownership AFTER acquiring serialization.
		s.writeLock.Lock()
		s.ioLock.Lock()
		if a != s.serverAttempt || a.closed || a.pending[key] != p || ctx.Err() != nil {
			if a.pending[key] == p {
				delete(a.pending, key)
			}
			s.ioLock.Unlock()
			s.writeLock.Unlock()
			s.observeServerReply(request, JsonRpcReplyDiscarded, context.Canceled)
			return
		}
		stdin := s.stdin
		s.ioLock.Unlock()
		encoded, err := encodeServerReply(id, result, rpcErr)
		status := JsonRpcReplyDiscarded
		if err == nil && stdin != nil {
			status = JsonRpcReplyUnknown
			// Cancellation may interrupt a blocked OS-pipe write without taking
			// writeLock or ioLock. The captured writer can never become a successor.
			stopClose := context.AfterFunc(ctx, func() { _ = stdin.Close() })
			err = writeRPCFrame(stdin, encoded)
			stopClose()
			if err == nil {
				status = JsonRpcReplySent
			}
		} else if err == nil {
			err = ErrNoInputChannel
		}
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
