package subprocess

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"os"
	"testing"
	"time"
)

func hostParamsFixture(t *testing.T, dto string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../protocol/v2/fixtures/host-rpc.json")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Structural []struct {
			Schema, Raw string
			Valid       bool
		}
	}
	_ = json.Unmarshal(raw, &c)
	for _, v := range c.Structural {
		if v.Schema == dto && v.Valid {
			return json.RawMessage(v.Raw)
		}
	}
	t.Fatal("missing host params fixture")
	return nil
}
func TestReverseQueuedCancellationIsNotStarted(t *testing.T) {
	core := newFixtureCorrelation(true)
	defer core.close(nil)
	s := server{}
	core.encode = s.encodeFrame
	out := &gatedWriter{started: make(chan string, 4), release: make(chan struct{}, 4)}
	writer := newFrameWriter(out, time.Second, func() {})
	defer writer.abort(errConnectionClosed)
	_ = writer.submit([]byte("{}\n"), nil)
	<-out.started
	receipt := make(chan error, 1)
	core.publishCall = func(ctx context.Context, frame []byte, done func(error), start func(), completed func() bool, prepare func() ([]byte, error)) error {
		return writer.submitCancellable(ctx, frame, func(err error) { done(err); receipt <- err }, start, completed, prepare)
	}
	var controls int
	core.publishControl = func([]byte, func(error)) error { controls++; return nil }
	ctx, cancel := context.WithCancelCause(context.Background())
	result, err := core.callContext(ctx, "host/storage/put", hostParamsFixture(t, "StoragePutParams"))
	if err != nil {
		t.Fatal(err)
	}
	cancel(&TransportCancelledError{Reason: CallerCancelled})
	reply := <-result
	var failure *RPCTransportError
	if !errors.As(reply.err, &failure) || failure.Failure.Code != capability.Cancelled || failure.Failure.EffectState != capability.NotStarted {
		t.Fatalf("classification: %v", reply.err)
	}
	<-receipt
	if controls != 0 {
		t.Fatal("unpublished request generated cancel")
	}
	writer.mu.Lock()
	queued := len(writer.ordinary.queue)
	writer.mu.Unlock()
	if queued != 0 {
		t.Fatal("expired queued frame retained")
	}
	out.release <- struct{}{}
	writer.seal()
	<-writer.done
}
func TestReversePublishedMutationHasUnknownOutcome(t *testing.T) {
	core := newFixtureCorrelation(true)
	defer core.close(nil)
	s := server{}
	core.encode = s.encodeFrame
	core.publishCall = func(_ context.Context, _ []byte, done func(error), start func(), _ func() bool, _ func() ([]byte, error)) error {
		start()
		done(nil)
		return nil
	}
	control := make(chan []byte, 1)
	core.publishControl = func(frame []byte, done func(error)) error { control <- frame; done(nil); return nil }
	ctx, cancel := context.WithCancelCause(context.Background())
	result, err := core.callContext(ctx, "host/storage/put", hostParamsFixture(t, "StoragePutParams"))
	if err != nil {
		t.Fatal(err)
	}
	cancel(&TransportCancelledError{Reason: CallerCancelled})
	reply := <-result
	var failure *RPCTransportError
	if !errors.As(reply.err, &failure) || failure.Failure.Code != capability.UnknownOutcome || failure.Failure.EffectState != capability.Unknown {
		t.Fatalf("classification %v", reply.err)
	}
	req, fault := decodeEnvelope(<-control)
	if fault != nil || req.Method != "rpc/cancel" || req.ID != (RPCID{}) {
		t.Fatal("cancel envelope")
	}
	raw, _ := paramsJSON(req.Params)
	var p CancelParams
	if json.Unmarshal(raw, &p) != nil || p.RequestOwner != HostRPCOwnerPlugin || p.ID != NumberID(1) {
		t.Fatal("cancel correlation")
	}
}
func TestReverseOfferClipSlotsAndParentCancellation(t *testing.T) {
	core := newFixtureCorrelation(true)
	defer core.close(nil)
	s := server{}
	core.encode = s.encodeFrame
	core.methodTimeoutMS["host/log"] = 500 // Fixture-only offer ceiling.
	frames := make(chan []byte, ReverseHandlerSlots)
	core.publish = func(frame []byte, done func(error)) error { frames <- frame; done(nil); return nil }
	var calls []<-chan callResult
	for i := 0; i < ReverseHandlerSlots; i++ {
		ch, err := core.call("host/log", json.RawMessage(duplexLogParams))
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, ch)
		req, _ := decodeEnvelope(<-frames)
		raw, _ := paramsJSON(req.Params)
		var params LogParams
		_ = json.Unmarshal(raw, &params)
		if params.Context.TimeoutMS > 500 || params.Context.TimeoutMS == 0 {
			t.Fatal("offered ceiling extended")
		}
	}
	if _, err := core.call("host/log", json.RawMessage(duplexLogParams)); err == nil {
		t.Fatal("ninth reverse request admitted")
	}
	core.close(errConnectionClosed)
	for _, ch := range calls {
		<-ch
	}
	a, _ := admissionFixture(t)
	parent := beginFixture(t, a, NumberID(1), MethodHealth, nil)
	parent.start()
	child := newFixtureCorrelation(true)
	defer child.close(nil)
	child.encode = s.encodeFrame
	child.publish = func([]byte, func(error)) error { return nil }
	control := make(chan []byte, 1)
	child.publishControl = func(raw []byte, done func(error)) error { control <- raw; done(nil); return nil }
	ch, err := child.callContext(parent.ctx, "host/log", json.RawMessage(duplexLogParams))
	if err != nil {
		t.Fatal(err)
	}
	a.cancelRequest(CancelParams{HostRPCOwnerHost, parent.id, CallerCancelled})
	<-parent.terminalDone
	reply := <-ch
	var failure *RPCTransportError
	if !errors.As(reply.err, &failure) || failure.Failure.Code != capability.Cancelled {
		t.Fatal("child survived parent cancellation")
	}
	request, _ := decodeEnvelope(<-control)
	raw, _ := paramsJSON(request.Params)
	var cancel CancelParams
	_ = json.Unmarshal(raw, &cancel)
	if cancel.Reason != ParentCancelled {
		t.Fatal("missing descendant cancellation reason")
	}
	parent.finish()
}

func TestSharedReverseEffectRecipe(t *testing.T) {
	raw, err := os.ReadFile("../protocol/v2/fixtures/duplex-effects.json")
	if err != nil {
		t.Fatal(err)
	}
	var recipe struct {
		Cases []struct {
			Name, Method string
			Params       string `json:"params_raw"`
			Possible     bool
			Code         capability.Code
			State        capability.EffectState `json:"effect_state"`
		}
	}
	if json.Unmarshal(raw, &recipe) != nil {
		t.Fatal("effects corpus")
	}
	for _, v := range recipe.Cases {
		t.Run(v.Name, func(t *testing.T) {
			core := newFixtureCorrelation(true)
			defer core.close(nil)
			s := server{}
			core.encode = s.encodeFrame
			core.publishCall = func(_ context.Context, _ []byte, done func(error), start func(), _ func() bool, _ func() ([]byte, error)) error {
				if v.Possible {
					start()
				}
				done(nil)
				return nil
			}
			core.publishControl = func([]byte, func(error)) error { return nil }
			ctx, cancel := context.WithCancelCause(context.Background())
			ch, err := core.callContext(ctx, v.Method, json.RawMessage(v.Params))
			if err != nil {
				t.Fatal(err)
			}
			cancel(&TransportCancelledError{Reason: CallerCancelled})
			result := <-ch
			var failure *RPCTransportError
			if !errors.As(result.err, &failure) || failure.Failure.Code != v.Code || failure.Failure.EffectState != v.State {
				t.Fatalf("wrong classification: %v", result.err)
			}
		})
	}
}

// A delayed timer can leave Done open past Deadline. The writer must still use
// the monotonic boundary, not wait for that scheduling event.
type delayedDeadlineContext struct {
	context.Context
	end time.Time
}

func (c delayedDeadlineContext) Deadline() (time.Time, bool) { return c.end, true }
func TestWriterRejectsExpiredClockBeforeTimerDelivery(t *testing.T) {
	out := &gatedWriter{started: make(chan string, 1), release: make(chan struct{})}
	writer := newFrameWriter(out, time.Second, func() {})
	defer writer.abort(errConnectionClosed)
	ctx := delayedDeadlineContext{context.Background(), time.Now().Add(-time.Second)}
	err := writer.submitCancellable(ctx, []byte("{}\n"), nil, func() { t.Error("expired publication selected") }, func() bool { return false }, nil)
	var expired *DeadlineExceededError
	if !errors.As(err, &expired) {
		t.Fatalf("deadline check: %v", err)
	}
	writer.seal()
	<-writer.done
}

func TestReverseQueuedPublicationDeductsElapsedBudget(t *testing.T) {
	core := newFixtureCorrelation(true)
	defer core.close(nil)
	s := server{}
	core.encode = s.encodeFrame
	writer, out := writerRig(t, QueueLimits{DefaultQueueFrames, DefaultQueuedWriteBytes})
	if err := writer.submit([]byte("{}\n"), nil); err != nil {
		t.Fatal(err)
	}
	startedFrame(t, out)
	var initial uint32
	core.publishCall = func(ctx context.Context, frame []byte, done func(error), start func(), completed func() bool, prepare func() ([]byte, error)) error {
		var request struct {
			Params LogParams `json:"params"`
		}
		if err := json.Unmarshal(frame, &request); err != nil {
			t.Fatal(err)
		}
		initial = request.Params.Context.TimeoutMS
		return writer.submitCancellable(ctx, frame, done, start, completed, prepare)
	}
	result, err := core.callContext(context.Background(), "host/log", hostParamsFixture(t, "LogParams"))
	if err != nil {
		t.Fatal(err)
	}
	// Fixture-only delay while a preceding write is held. No production timeout default.
	<-time.After(20 * time.Millisecond)
	out.release <- struct{}{}
	frame := startedFrame(t, out)
	var request struct {
		Params LogParams `json:"params"`
	}
	if err := json.Unmarshal([]byte(frame), &request); err != nil {
		t.Fatal(err)
	}
	remaining := request.Params.Context.TimeoutMS
	if remaining == 0 || remaining >= initial {
		t.Fatalf("wire timeout did not deduct queue time: %d -> %d", initial, remaining)
	}
	out.release <- struct{}{}
	core.close(errConnectionClosed)
	<-result
}
