package pluginhooks

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func remoteFixture(t *testing.T, kind Kind, mode Mode) (*Registry, *Engine, *Scope, *RemoteConnection, Definition) {
	t.Helper()
	d := definition("remote.event", kind)
	d.Mode = mode
	if kind == Filter {
		d.InputSchema = json.RawMessage(`{}`)
		d.OutputSchema = json.RawMessage(`{}`)
		d.MutablePaths = []string{""}
	}
	*d.RemoteOK = true
	d.RemoteLatencyBudget = 100 * time.Millisecond
	r, err := NewRegistry(Catalog{Version: "test", Definitions: []Definition{d}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(r, ExecutionConfig{MaxActive: 1, MaxActivePerOwner: 1, Breaker: BreakerConfig{FailureThreshold: 1}})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := r.NewScope(ScopeConfig{Owner: "owner", Generation: "one", Hooks: []string{d.Name}, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewRemoteConnection()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := scope.Dispose(ctx); err != nil {
			t.Error(err)
		}
		if err := e.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return r, e, scope, c, d
}
func TestRemoteDeadlineRetainsCapacity(t *testing.T) {
	_, e, s, c, d := remoteFixture(t, Action, Sequential)
	release, started := make(chan struct{}), make(chan struct{})
	defer close(release)
	timeout := 20 * time.Millisecond
	h, err := s.AddRemoteAction(d.Name, "slow", Options{Timeout: &timeout}, RemoteRegistration{Connection: c, LatencyEstimate: time.Millisecond, Handler: RemoteHandlerFunc(func(ctx context.Context, q RemoteRequest) (RemoteResult, error) {
		deadline, _ := ctx.Deadline()
		if q.Context.Deadline != deadline || q.Context.Timeout > timeout {
			t.Error("unclipped timeout")
		}
		close(started)
		<-release
		return RemoteResult{InvocationID: q.InvocationID, Status: RemoteOK}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, dispatchErr := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
		done <- dispatchErr
	}()
	<-started
	select {
	case dispatchErr := <-done:
		if !errors.Is(dispatchErr, context.DeadlineExceeded) {
			t.Fatal(dispatchErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout did not release caller")
	}
	s.Remove(h)
	if resetErr := e.ResetBreaker("owner", "one"); resetErr != nil {
		t.Fatal(resetErr)
	}
	var calls atomic.Int32
	_, err = s.AddRemoteAction(d.Name, "once", Options{Once: true, Timeout: &timeout}, RemoteRegistration{Connection: c, LatencyEstimate: time.Millisecond, Handler: RemoteHandlerFunc(func(_ context.Context, q RemoteRequest) (RemoteResult, error) {
		calls.Add(1)
		return RemoteResult{InvocationID: q.InvocationID, Status: RemoteOK}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, dispatchErr := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil); !errors.Is(dispatchErr, ErrUnavailable) || calls.Load() != 0 || len(s.Registrations()) != 1 {
		t.Fatalf("timeout released permits/consumed once: %v calls=%d", dispatchErr, calls.Load())
	}
	snapshot, err := e.Breaker("owner", "one")
	if err != nil || snapshot.ConsecutiveFailures != 0 {
		t.Fatalf("capacity admission counted as failure %+v %v", snapshot, err)
	}
}
func TestRemoteCancellationAndConnectionExcluded(t *testing.T) {
	for _, kind := range []string{"caller", "connection", "unload"} {
		t.Run(kind, func(t *testing.T) {
			_, e, s, c, d := remoteFixture(t, Action, Sequential)
			started := make(chan struct{})
			_, err := s.AddRemoteAction(d.Name, "wait", Options{}, RemoteRegistration{Connection: c, LatencyEstimate: time.Millisecond, Handler: RemoteHandlerFunc(func(ctx context.Context, _ RemoteRequest) (RemoteResult, error) {
				close(started)
				<-ctx.Done()
				return RemoteResult{}, ctx.Err()
			})})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan DispatchResult, 1)
			go func() { r, _ := e.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil); done <- r }()
			<-started
			switch kind {
			case "caller":
				cancel()
			case "connection":
				c.Close()
			case "unload":
				if disposeErr := s.Dispose(context.Background()); disposeErr != nil {
					t.Fatal(disposeErr)
				}
			}
			select {
			case result := <-done:
				if kind == "caller" && result.Status != CallerCancelled {
					t.Fatal(result)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation stuck")
			}
			snapshot, err := e.Breaker("owner", "one")
			if err != nil || snapshot.ConsecutiveFailures != 0 {
				t.Fatalf("cancellation counted %+v %v", snapshot, err)
			}
		})
	}
}
func TestRemoteFilterValidationAndFailureBranches(t *testing.T) {
	for _, test := range []struct {
		name   string
		result RemoteResult
		want   error
	}{
		{"missing-filter-output", RemoteResult{Status: RemoteOK}, ErrInvalidOutput},
		{"invalid-json", RemoteResult{Status: RemoteOK, Payload: json.RawMessage(`{`)}, ErrInvalidOutput},
		{"illegal-filter-veto", RemoteResult{Status: RemoteCancelled}, ErrInvalidOutput},
		{"contradictory-success", RemoteResult{Status: RemoteOK, Payload: json.RawMessage(`{}`), Failure: &RemoteFailure{Code: HandlerError}}, ErrInvalidOutput},
		{"null-is-present", RemoteResult{Status: RemoteOK, Payload: json.RawMessage(`null`)}, nil},
		{"operational-cancellation-is-not-veto", RemoteResult{Status: RemoteFailed, Failure: &RemoteFailure{Code: CallerCancellation}}, context.Canceled},
		{"reported-depth-is-handler-failure", RemoteResult{Status: RemoteFailed, Failure: &RemoteFailure{Code: DepthExceeded}}, ErrDepthExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, e, s, c, d := remoteFixture(t, Filter, Waterfall)
			_, err := s.AddRemoteFilter(d.Name, "result", Options{}, RemoteRegistration{Connection: c, LatencyEstimate: time.Millisecond, Handler: RemoteHandlerFunc(func(_ context.Context, q RemoteRequest) (RemoteResult, error) {
				result := test.result
				result.InvocationID = q.InvocationID
				return result, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			result, err := e.ApplyFilters(context.Background(), d.Name, json.RawMessage(`{}`), nil)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
			if test.want == nil {
				if string(result.Value) != "null" {
					t.Fatal(result)
				}
			} else if result.Value != nil || result.Status != FailedClosed {
				t.Fatal(result)
			}
			if test.want != nil {
				snapshot, _ := e.Breaker("owner", "one")
				if snapshot.State != BreakerOpen {
					t.Fatalf("remote failure did not open breaker: %+v", snapshot)
				}
			}
		})
	}
}
func TestRemoteResultFailureCodes(t *testing.T) {
	d := definition("gate.check", Action)
	d.Mode = Bail
	for _, code := range []RemoteFailureCode{RemoteNotAllowed, LatencyBudgetExceeded, StaleScope, StaleBinding, CapacityExhausted, DeadlineExceeded, CallerCancellation, DepthExceeded, CallbackCycle, TransportFailure, HandlerPanic, InvalidOutput, HandlerError, SchemaMismatch, ProfileUnavailable} {
		_, err := validateRemoteResult(d, "id", RemoteResult{InvocationID: "id", Status: RemoteFailed, Failure: &RemoteFailure{Code: code, Message: "hook approval required"}})
		var failure *RemoteFailure
		if !errors.As(err, &failure) || failure.Code != code || errors.Is(err, ErrCancelled) || errors.Is(err, ErrApprovalRequired) {
			t.Fatalf("code %s mapped as veto: %v", code, err)
		}
	}
}
func TestRemotePanicCountsAndLatencyRefusalDoesNot(t *testing.T) {
	_, e, s, c, d := remoteFixture(t, Action, Sequential)
	r := RemoteRegistration{Connection: c, LatencyEstimate: 50 * time.Millisecond, Handler: RemoteHandlerFunc(func(context.Context, RemoteRequest) (RemoteResult, error) { panic("transport adapter panic") })}
	_, err := s.AddRemoteAction(d.Name, "panic", Options{}, r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := e.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil)
	var failure *RemoteFailure
	if !errors.As(err, &failure) || failure.Code != LatencyBudgetExceeded || result.Status != FailedClosed {
		t.Fatalf("latency refusal %+v %v", result, err)
	}
	snapshot, _ := e.Breaker("owner", "one")
	if snapshot.ConsecutiveFailures != 0 {
		t.Fatal(snapshot)
	}
	if _, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil); !errors.Is(err, ErrPanic) {
		t.Fatal(err)
	}
	snapshot, _ = e.Breaker("owner", "one")
	if snapshot.State != BreakerOpen || snapshot.LastFailureClass != "panic" {
		t.Fatal(snapshot)
	}
}
