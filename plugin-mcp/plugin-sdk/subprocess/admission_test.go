package subprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"os"
	"testing"
	"time"
)

func admissionFixture(t *testing.T) (*admission, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	writer := newFrameWriter(out, time.Second, func() {})
	core := newCorrelation(false)
	server := &server{outputLimit: DefaultFrameBytes, fence: func(err error) { t.Errorf("fence: %v", err) }}
	a := newAdmission(writer, core, server)
	t.Cleanup(func() { writer.abort(errConnectionClosed); <-writer.done })
	return a, out
}
func beginFixture(t *testing.T, a *admission, id RPCID, method string, params any) *requestScope {
	t.Helper()
	if err := a.core.admit(id); err != nil {
		t.Fatal(err)
	}
	s, err := a.begin(context.Background(), RPCRequest{ID: id, Method: method, Params: params}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestAdmissionRetainsCancelledPermitsAndOldIdentity(t *testing.T) {
	a, out := admissionFixture(t)
	scopes := make([]*requestScope, ForwardHandlerSlots)
	for i := range scopes {
		scopes[i] = beginFixture(t, a, NumberID(int64(i+1)), MethodHealth, nil)
		if !scopes[i].start() {
			t.Fatal("not started")
		}
	}
	if _, err := a.begin(context.Background(), RPCRequest{ID: StringID("overload"), Method: MethodHealth}, time.Now()); err == nil {
		t.Fatal("17th handler admitted")
	}
	first := scopes[0]
	a.cancelRequest(CancelParams{HostRPCOwnerPlugin, first.id, CallerCancelled})
	if first.ctx.Err() != nil {
		t.Fatal("wrong-owner cancellation")
	}
	a.cancelRequest(CancelParams{HostRPCOwnerHost, NumberID(999), CallerCancelled})
	a.cancelRequest(CancelParams{HostRPCOwnerHost, first.id, CallerCancelled})
	<-first.terminalDone
	var reply RPCResponse
	if json.Unmarshal(out.Bytes(), &reply) != nil || reply.Error == nil {
		t.Fatal("missing cancelled reply")
	}
	data, _ := json.Marshal(reply.Error.Data)
	var classified HostRPCErrorData
	_ = json.Unmarshal(data, &classified)
	if classified.Code != capability.Cancelled || classified.EffectState != capability.Unknown {
		t.Fatalf("classification %s", data)
	}
	if _, err := a.begin(context.Background(), RPCRequest{ID: first.id, Method: MethodHealth}, time.Now()); err == nil {
		t.Fatal("uncooperative permit released")
	}
	scopes[1].reply(RPCResponse{JSONRPC: "2.0", ID: scopes[1].id, Result: json.RawMessage(`{"ok":true}`)})
	<-scopes[1].terminalDone
	scopes[1].finish()
	<-scopes[1].executionDone
	reused := beginFixture(t, a, first.id, MethodHealth, nil)
	reused.start()
	before := out.Len()
	first.reply(RPCResponse{JSONRPC: "2.0", ID: first.id, Result: json.RawMessage(`{"ok":false}`)})
	if out.Len() != before {
		t.Fatal("late old callback published")
	}
	first.finish()
	<-first.executionDone
	reused.reply(RPCResponse{JSONRPC: "2.0", ID: reused.id, Result: json.RawMessage(`{"ok":true}`)})
	<-reused.terminalDone
	reused.finish()
	for _, scope := range scopes[2:] {
		scope.reply(RPCResponse{JSONRPC: "2.0", ID: scope.id, Result: json.RawMessage(`{"ok":true}`)})
		<-scope.terminalDone
		scope.finish()
	}
}
func TestAdmissionLifecycleReserveAndTerminalArithmetic(t *testing.T) {
	a, _ := admissionFixture(t)
	var scopes []*requestScope
	for i := 0; i < ForwardHandlerSlots; i++ {
		scopes = append(scopes, beginFixture(t, a, NumberID(int64(i+1)), MethodHealth, nil))
	}
	for i := 0; i < ControlHandlerSlots; i++ {
		scopes = append(scopes, beginFixture(t, a, NumberID(int64(100+i)), MethodLoad, nil))
	}
	if _, err := a.begin(context.Background(), RPCRequest{ID: NumberID(200), Method: MethodLoad}, time.Now()); err == nil {
		t.Fatal("control reserve borrowed")
	}
	if ForwardHandlerSlots+ReverseHandlerSlots+ControlHandlerSlots > DefaultQueueFrames || (ForwardHandlerSlots+ReverseHandlerSlots+ControlHandlerSlots)*TerminalCreditBytes > DefaultQueuedWriteBytes {
		t.Fatal("terminal arithmetic")
	}
	for _, s := range scopes {
		s.reply(requestFailureResponse(s.id, capability.Cancelled, capability.NotStarted))
		<-s.terminalDone
		s.finish()
	}
}
func TestAdmissionDeadlinesStartAtArrivalAndNoDefault(t *testing.T) {
	a, _ := admissionFixture(t)
	req := RPCRequest{ID: NumberID(1), Method: MethodHealth, Params: json.RawMessage(`{"context":{"timeout_ms":1}}`)}
	_ = a.core.admit(req.ID)
	// Fixture-only 1 ms budget has already expired before validation/admission.
	s, err := a.begin(context.Background(), req, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.start() {
		t.Fatal("expired request executed")
	}
	<-s.terminalDone
	s.finish()
	plain := beginFixture(t, a, NumberID(2), MethodHealth, nil)
	if _, ok := plain.ctx.Deadline(); ok {
		t.Fatal("SDK invented a deadline")
	}
	release := plain.retain()
	plain.finish()
	select {
	case <-plain.executionDone:
		t.Fatal("retained hook released permit")
	default:
	}
	release()
	<-plain.executionDone
	plain.reply(RPCResponse{JSONRPC: "2.0", ID: plain.id, Result: json.RawMessage(`{"ok":true}`)})
	<-plain.terminalDone
}
func TestAdmissionOversizedCommittedResult(t *testing.T) {
	a, out := admissionFixture(t)
	a.server.outputLimit = 512
	s := beginFixture(t, a, StringID("write"), MethodHealth, nil)
	s.start()
	result, _ := json.Marshal(map[string]string{"body": string(bytes.Repeat([]byte("x"), 2048))})
	s.reply(RPCResponse{JSONRPC: "2.0", ID: s.id, Result: result})
	<-s.terminalDone
	s.finish()
	var response RPCResponse
	_ = json.Unmarshal(out.Bytes(), &response)
	raw, _ := json.Marshal(response.Error.Data)
	var failure PluginRPCErrorData
	if json.Unmarshal(raw, &failure) != nil || failure.Code != capability.BudgetExceeded || failure.EffectState != capability.Committed || out.Len() > TerminalCreditBytes {
		t.Fatalf("bad fallback %s", out.Bytes())
	}
}

func TestControlNotificationRoutesWithoutHandlerAdmission(t *testing.T) {
	started, release := make(chan context.Context, 1), make(chan struct{})
	p := &shutdownPlugin{commandFn: func(ctx context.Context, _ CommandRequest) (CommandResult, error) {
		started <- ctx
		<-release
		return CommandResult{Action: "noop"}, nil
	}}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	rig := newDuplexRig(t, p, newCorrelation(false))
	rig.send(initLine(t, 1))
	rig.read()
	rig.send(`{"jsonrpc":"2.0","id":"held","method":"command/execute","params":{"name":"hold","session_id":"","args":""}}`)
	handler := <-started
	rig.send(`{"jsonrpc":"2.0","method":"rpc/cancel","params":{"request_owner":"plugin","id":"held","reason":"caller_cancelled"}}`)
	rig.send(`{"jsonrpc":"2.0","method":"rpc/cancel","params":{"request_owner":"host","id":"held","reason":"unknown"}}`)
	rig.send(`{"jsonrpc":"2.0","id":2,"method":"plugin/health"}`)
	rig.read()
	if handler.Err() != nil {
		t.Fatal("invalid/wrong-owner notification took effect")
	}
	rig.send(`{"jsonrpc":"2.0","method":"rpc/cancel","params":{"request_owner":"host","id":"held","reason":"caller_cancelled"}}`)
	reply := rig.read()
	var fault struct {
		Code int
		Data PluginRPCErrorData
	}
	if json.Unmarshal(reply["error"], &fault) != nil || fault.Code != ErrCodeInternal || fault.Data.Code != capability.UnknownOutcome {
		t.Fatal("missing closed base cancellation data")
	}
	if handler.Err() == nil {
		t.Fatal("callback not cancelled")
	}
	rig.send(`{"jsonrpc":"2.0","id":3,"method":"plugin/health"}`)
	rig.read()
	close(release)
	rig.input.Close()
	if err := <-rig.done; err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionLimitsNarrowDefaults(t *testing.T) {
	for _, limits := range []AdmissionLimits{{Forward: -1}, {Forward: 17}, {Reverse: 9}, {Control: 3}} {
		if _, err := admissionLimits(limits); err == nil {
			t.Fatal("widened/invalid admission limits accepted")
		}
	}
	limits, err := admissionLimits(AdmissionLimits{Forward: 1, Reverse: 1, Control: 1})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := admissionFixture(t)
	a.limits = limits
	s := beginFixture(t, a, NumberID(1), MethodHealth, nil)
	if _, err := a.begin(context.Background(), RPCRequest{ID: NumberID(2), Method: MethodHealth}, time.Now()); err == nil {
		t.Fatal("narrowed handler limit ignored")
	}
	s.reply(RPCResponse{JSONRPC: "2.0", ID: s.id, Result: json.RawMessage(`{"ok":true}`)})
	<-s.terminalDone
	s.finish()
}

func TestSharedDeadlineNoDefaultsRecipe(t *testing.T) {
	raw, err := os.ReadFile("../protocol/v2/fixtures/duplex-deadlines.json")
	if err != nil {
		t.Fatal(err)
	}
	var recipe struct {
		Methods []struct {
			Method string
			Params string `json:"params_raw"`
		} `json:"no_default_methods"`
	}
	if json.Unmarshal(raw, &recipe) != nil {
		t.Fatal("deadline corpus")
	}
	for _, v := range recipe.Methods {
		t.Run(v.Method, func(t *testing.T) {
			a, _ := admissionFixture(t)
			var params any
			if v.Params != "" {
				params = json.RawMessage(v.Params)
			} else if v.Method == MethodInit {
				params = validInitParams()
			}
			s := beginFixture(t, a, NumberID(1), v.Method, params)
			if _, ok := s.ctx.Deadline(); ok {
				t.Fatal("default method deadline invented")
			}
			s.reply(RPCResponse{JSONRPC: "2.0", ID: s.id, Result: json.RawMessage(`{}`)})
			<-s.terminalDone
			s.finish()
		})
	}
}
