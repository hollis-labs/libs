package gatecrasher

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
	"github.com/hollis-labs/libs/mesh/core/hitl"
)

// These are provider-shaped HOST projections of synthetic operation facts,
// anchored to the checked-in harness examples. They test the coordinator only,
// not official protocol conformance, provider callback reachability or a parser.
// GC-2 owns pinned real protocol envelopes, async request loops and UI adapters.
func TestNativeCommandAndMCPHostProjectionsRetainExactChoices(t *testing.T) {
	for _, tc := range []struct{ name, method, class, descriptor, allow, deny, payload, id string }{
		{"codex-command", "item/commandExecution/requestApproval", "command-escalation", `{"v":1,"command":["echo","fixture"],"cwd":"fixture-workspace","sandbox":"read-only","effect":"process","environment":"environment-1","resourceVersion":"1"}`, "accept", "decline", `{"decision":"accept"}`, `9007199254740993`},
		{"codex-mcp-tool", "mcpServer/elicitation/request", "mcp-tool-approval", `{"v":1,"resolvedServer":"fixture-server","resolvedTool":"echo","actualArguments":{"value":1},"environment":"environment-1","resourceVersion":"1","effect":"read"}`, "accept", "decline", `{"action":"accept","content":{}}`, `"server-request-1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			f.request.OperationClass = tc.class
			f.request.Adapter.Protocol = tc.method
			f.request.NativeRequestID = gate.NativeRequestID(tc.id)
			f.request.OperationDigest = gatetest.Digest("host-native-fixture-v1", tc.descriptor)
			f.request.Options[0].ID = tc.allow
			f.request.Options[0].Payload = json.RawMessage(tc.payload)
			f.request.Options[1].ID = tc.deny
			f.origin.live = f.request
			f.origin.cap = gatetest.Capabilities(f.request)
			f.origin.cap.OperationClasses = []string{tc.class}
			ticket := f.ticket(t)
			res := f.respond(t, ticket, tc.allow)
			if res.Decision.OptionID != tc.allow {
				t.Fatal(res)
			}
			p, err := f.c.Deliver(human(), ticket)
			if err != nil || !p.NativeRequestID.Equal(gate.NativeRequestID(tc.id)) {
				t.Fatal(p, err)
			}
			route, _ := f.journal.Load(human(), f.request.IdempotencyKey)
			if string(route.Request.Options[0].Payload) != tc.payload {
				t.Fatal("native payload lost")
			}
		})
	}
}

func TestCallbackReachabilityIsExplicitCapabilityNotHookPresence(t *testing.T) {
	for _, mode := range []string{"plan-never", "sandbox-local-no-callback", "danger-full-access-never", "general-elicitation"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture()
			f.request.Adapter.LaunchMode = mode
			f.origin.live = f.request
			f.origin.cap = gatetest.Capabilities(f.request)
			// The fixture adapter declares there is no native gated operation to serve.
			// This is a negative control, not a statement about installed providers.
			f.origin.cap.OperationClasses = nil
			if _, err := f.c.Request(human(), f.request); !errors.Is(err, gate.ErrUnsupported) || f.origin.sends != 0 {
				t.Fatal(err)
			}
		})
	}
}

func TestACPExactOptionAndTurnCancellationStayDistinct(t *testing.T) {
	f := newFixture()
	f.request.Adapter.Protocol = "session/request_permission"
	f.request.Options[0].ID = "allow-1"
	f.request.Options[1].ID = "deny-1"
	f.origin.live = f.request
	f.origin.cap = gatetest.Capabilities(f.request)
	ticket := f.ticket(t)
	if _, err := f.c.Respond(human(), ticket, ResponseAttempt{OptionID: "Allow once", ExpectedRevision: ticket.Handle.Revision}); !errors.Is(err, gate.ErrUnsupported) {
		t.Fatal("display label became an option identity", err)
	}
	res := f.respond(t, ticket, "deny-1")
	if res.State != gate.Decided || res.Decision.OptionID != "deny-1" {
		t.Fatal(res)
	}
	f = newFixture()
	f.request.Adapter.Protocol = "session/request_permission"
	f.origin.live = f.request
	f.origin.cap = gatetest.Capabilities(f.request)
	ticket = f.ticket(t)
	cancelled, err := f.c.Cancel(human(), ticket, hitl.CauseCallerWithdrawn)
	if err != nil || cancelled.State != gate.Cancelled || cancelled.Decision != nil {
		t.Fatal(cancelled, err)
	}
	p, err := f.c.Deliver(human(), ticket)
	if err != nil || p.Status != gate.DeliveryAcknowledged {
		t.Fatal(p, err)
	}
	// allow_always/reject_always are never inferred from a label as bounded.
	f = newFixture()
	f.request.Options[0].ID = "allow_always"
	f.request.Options[0].Scope.Kind = gate.Session
	f.origin.live = f.request
	if _, err := f.c.Request(human(), f.request); !errors.Is(err, gate.ErrUnsupported) {
		t.Fatal(err)
	}
}
