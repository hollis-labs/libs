package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	sdksub "github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

type writeFunc func([]byte) (int, error)

func (f writeFunc) Write(p []byte) (int, error) { return f(p) }

func TestTypedRPCIDRoundTripAndRegistrationBeforeWrite(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	writer := writeFunc(func(raw []byte) (int, error) {
		var request sdksub.RPCRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return 0, err
		}
		if n, ok := request.ID.Integer(); !ok || n != 1 {
			t.Errorf("ID=%v", request.ID)
		}
		reply, err := json.Marshal(sdksub.RPCResponse{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"ok":true}`)})
		if err != nil {
			return 0, err
		}
		_, err = w.Write(append(reply, '\n'))
		return len(raw), err
	})
	transport := newRPCTransport(r, writer)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := callResult[sdksub.HealthResult](ctx, transport, sdksub.MethodHealth, nil)
	if err != nil || !result.OK {
		t.Fatalf("roundtrip=%+v %v", result, err)
	}
}

func TestResponseEnvelopeClosedAndTyped(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":1,"result":null}`
	if _, err := decodeResponse([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"jsonrpc":"1.0","id":1,"result":null}`,
		`{"jsonrpc":"2.0","id":1,"id":1,"result":null}`,
		`{"jsonrpc":"2.0","ID":1,"result":null}`,
		`{"jsonrpc":"2.0","id":"1","result":null}`,
		`{"jsonrpc":"2.0","id":0,"result":null}`,
		`{"jsonrpc":"2.0","id":9007199254740992,"result":null}`,
		`{"jsonrpc":"2.0","id":1,"result":null,"error":null}`,
		`{"jsonrpc":"2.0","id":1,"error":null}`,
		`{"jsonrpc":"2.0","id":1}`,
		`{"jsonrpc":"2.0","id":1,"method":"host/storage.get","params":{}}`,
		valid + valid,
	} {
		if _, err := decodeResponse([]byte(raw)); err == nil {
			t.Fatalf("invalid envelope accepted: %s", raw)
		}
	}
}

func TestRPCSafeIDExhaustionAndShortWriteFence(t *testing.T) {
	tr := newRPCTransport(strings.NewReader(""), io.Discard)
	tr.nextID.Store(9007199254740991)
	if _, err := tr.call(context.Background(), sdksub.MethodHealth, nil); err == nil {
		t.Fatal("unsafe ID accepted")
	}
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	short := newRPCTransport(r, writeFunc(func(p []byte) (int, error) { return len(p) - 1, nil }))
	_, err := short.call(context.Background(), sdksub.MethodHealth, nil)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("shortwrite=%v", err)
	}
	select {
	case <-short.Done():
	default:
		t.Fatal("partial publication not fenced")
	}
	short.mu.Lock()
	n := len(short.pending)
	short.mu.Unlock()
	if n != 0 {
		t.Fatalf("pending=%d", n)
	}
}

func TestRPCMalformedResponseClosesConnection(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	tr := newRPCTransport(r, writeFunc(func(p []byte) (int, error) {
		_, err := w.Write([]byte(`{"jsonrpc":"2.0","id":1,"id":1,"result":{}}` + "\n"))
		return len(p), err
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := tr.call(ctx, sdksub.MethodHealth, nil); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("malformed reply=%v", err)
	}
}
