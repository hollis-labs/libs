package inprocess

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	sdksub "github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

// ErrGone reports that the plugin subprocess's pipe closed — exited,
// crashed, or killed — while a call was outstanding or before one was
// sent.
var ErrGone = errors.New("inprocess: plugin is gone")

// rpcTransport drives plugin-sdk/subprocess's JSON-RPC dialect as the
// host/client side: newline-delimited JSON-RPC 2.0 requests on the
// subprocess's stdin, responses read from its stdout and matched to
// their waiting caller by request ID. plugin-sdk ships the plugin side
// of this dialect (subprocess.Serve) but not a host-side driver — this
// is mcp-host's own reimplementation of the shape Nanite's
// internal/plugin/subprocess.Transport already proves out (the ADR
// explicitly calls for reimplementing the pattern rather than importing
// Nanite).
type rpcTransport struct {
	w io.Writer
	r *bufio.Reader

	nextID atomic.Int64

	mu       sync.Mutex
	pending  map[sdksub.RPCID]chan *sdksub.RPCResponse
	closedBy error

	writeMu sync.Mutex
	done    chan struct{}
}

// newRPCTransport wraps r/w (the plugin subprocess's stdout/stdin pipes)
// and starts the background reader loop routing responses to callers.
func newRPCTransport(r io.Reader, w io.Writer) *rpcTransport {
	t := &rpcTransport{
		w:       w,
		r:       bufio.NewReaderSize(r, 64*1024),
		pending: make(map[sdksub.RPCID]chan *sdksub.RPCResponse),
		done:    make(chan struct{}),
	}
	go t.readLoop()
	return t
}

// Done returns a channel closed once the transport's read loop ends —
// the plugin's stdout pipe hit EOF or a read error, i.e. the subprocess
// is gone. The supervision loop blocks on this the same way T3's
// process-mode SpawnTransport blocks on an MCP ClientSession's Wait.
func (t *rpcTransport) Done() <-chan struct{} { return t.done }

// closeWriter closes the underlying stdin pipe, if it implements
// io.Closer — the JSON-RPC dialect equivalent of the MCP stdio shutdown
// sequence's "close the input stream to the child process" step. A
// well-behaved plugin's own read loop sees EOF and exits on its own;
// this is what lets Close's reap complete quickly instead of always
// riding out the full SIGTERM/SIGKILL escalation.
func (t *rpcTransport) closeWriter() {
	if closer, ok := t.w.(io.Closer); ok {
		_ = closer.Close()
	}
}

func (t *rpcTransport) call(ctx context.Context, method string, params any) (*sdksub.RPCResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := t.nextID.Add(1)
	if n <= 0 || n > 9007199254740991 {
		return nil, errors.New("inprocess: safe request IDs exhausted")
	}
	id := sdksub.NumberID(n)
	reply := make(chan *sdksub.RPCResponse, 1)

	t.mu.Lock()
	if t.closedBy != nil {
		err := t.closedBy
		t.mu.Unlock()
		return nil, fmt.Errorf("call %s: %w", method, err)
	}
	t.pending[id] = reply
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
	}()

	req := sdksub.RPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	if err := t.write(req); err != nil {
		return nil, fmt.Errorf("write %s: %w", method, err)
	}

	callCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	select {
	case resp := <-reply:
		if resp == nil {
			return nil, fmt.Errorf("read %s response: %w", method, ErrGone)
		}
		if resp.Error != nil {
			return resp, resp.Error
		}
		return resp, nil
	case <-callCtx.Done():
		return nil, fmt.Errorf("read %s response: %w", method, callCtx.Err())
	case <-t.done:
		t.mu.Lock()
		err := t.closedBy
		t.mu.Unlock()
		if err == nil {
			err = ErrGone
		}
		return nil, fmt.Errorf("read %s response: %w", method, err)
	}
}

func (t *rpcTransport) write(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	n, err := t.w.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		t.closeWith(err)
	}
	return err
}

func (t *rpcTransport) readLoop() {
	var readErr error
	for {
		line, err := t.r.ReadBytes('\n')
		if len(line) > 0 {
			if err != nil {
				readErr = errors.New("inprocess: partial response frame")
				break
			}
			t.deliver(line)
			select {
			case <-t.done:
				return
			default:
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
	}
	t.closeWith(readErr)
}

func (t *rpcTransport) deliver(line []byte) {
	resp, err := decodeResponse(line)
	if err != nil {
		t.closeWith(err)
		return
	}
	t.mu.Lock()
	reply, waiting := t.pending[resp.ID]
	t.mu.Unlock()
	if !waiting {
		return
	}
	select {
	case reply <- resp:
	default:
	}
}

// decodeResponse accepts only exact, duplicate-free reply envelopes. Incoming
// requests/notifications are unsupported on this forward-only connection.
func decodeResponse(line []byte) (*sdksub.RPCResponse, error) {
	bad := func() (*sdksub.RPCResponse, error) { return nil, errors.New("inprocess: invalid response envelope") }
	d := json.NewDecoder(bytes.NewReader(line))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return bad()
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return bad()
		}
		name, ok := token.(string)
		if !ok {
			return bad()
		}
		if _, exists := fields[name]; exists {
			return bad()
		}
		switch name {
		case "jsonrpc", "id", "result", "error":
		default:
			return bad()
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return bad()
		}
		fields[name] = raw
	}
	if _, err := d.Token(); err != nil {
		return bad()
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return bad()
	}
	var version string
	if json.Unmarshal(fields["jsonrpc"], &version) != nil || version != "2.0" {
		return bad()
	}
	var id sdksub.RPCID
	if json.Unmarshal(fields["id"], &id) != nil {
		return bad()
	}
	if n, ok := id.Integer(); !ok || n <= 0 {
		return bad()
	}
	result, hasResult := fields["result"]
	rawError, hasError := fields["error"]
	if hasResult == hasError {
		return bad()
	}
	resp := &sdksub.RPCResponse{JSONRPC: version, ID: id, Result: result}
	if hasError {
		var rpcError *sdksub.RPCError
		if json.Unmarshal(rawError, &rpcError) != nil || rpcError == nil {
			return bad()
		}
		resp.Error = rpcError
	}
	return resp, nil
}

func (t *rpcTransport) closeWith(cause error) {
	t.mu.Lock()
	if t.closedBy == nil {
		if cause != nil {
			t.closedBy = fmt.Errorf("%w: %w", ErrGone, cause)
		} else {
			t.closedBy = ErrGone
		}
		close(t.done)
	}
	t.pending = make(map[sdksub.RPCID]chan *sdksub.RPCResponse)
	t.mu.Unlock()
}

// callResult calls method and unmarshals the response's result into T.
func callResult[T any](ctx context.Context, t *rpcTransport, method string, params any) (*T, error) {
	resp, err := t.call(ctx, method, params)
	if err != nil {
		return nil, err
	}
	var result T
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("unmarshal %s result: %w", method, err)
	}
	return &result, nil
}
