package hitl_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/mesh/hitl"
	"github.com/hollis-labs/substrate/mesh/hitl/hitltest"
	"github.com/hollis-labs/substrate/mesh/hitl/schema"
)

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("left: %v", err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("right: %v", err)
	}
	return reflect.DeepEqual(x, y)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	for _, f := range hitltest.Fixtures() {
		if f.Name == name {
			return f.Doc
		}
	}
	t.Fatalf("no fixture %q", name)
	return nil
}

// Go round trip: valid fixture -> unmarshal -> marshal -> JSON-equal. Fixtures
// carrying members the Go type deliberately ignores (tolerant outputs) or
// keeping open extras are covered by their own tests below.
func TestValidFixturesRoundTripThroughGoTypes(t *testing.T) {
	decoders := map[string]func([]byte) ([]byte, error){
		"HITLTerminalOutcomeCoreV1": func(b []byte) ([]byte, error) {
			o, err := hitl.UnmarshalOutcome(b)
			if err != nil {
				return nil, err
			}
			return json.Marshal(o)
		},
		"HITLItemViewCoreV1": func(b []byte) ([]byte, error) {
			var v hitl.ItemView
			if err := json.Unmarshal(b, &v); err != nil {
				return nil, err
			}
			return json.Marshal(v)
		},
		"HITLGetRetrievalResultCoreV1": func(b []byte) ([]byte, error) {
			var v hitl.RetrievalResult
			if err := json.Unmarshal(b, &v); err != nil {
				return nil, err
			}
			return json.Marshal(v)
		},
		"HITLRetrievalResultCoreV1": func(b []byte) ([]byte, error) {
			var v hitl.RetrievalResult
			if err := json.Unmarshal(b, &v); err != nil {
				return nil, err
			}
			if err := v.Validate(); err != nil {
				return nil, err
			}
			return json.Marshal(v)
		},
		"HITLItemHandleCoreV1": func(b []byte) ([]byte, error) {
			var v hitl.Handle
			if err := json.Unmarshal(b, &v); err != nil {
				return nil, err
			}
			return json.Marshal(v)
		},
		"HITLResponseCoreV1": func(b []byte) ([]byte, error) {
			var v hitl.Response
			if err := json.Unmarshal(b, &v); err != nil {
				return nil, err
			}
			return json.Marshal(v)
		},
		"ProofV1": func(b []byte) ([]byte, error) {
			var v hitl.Proof
			if err := json.Unmarshal(b, &v); err != nil {
				return nil, err
			}
			return json.Marshal(v)
		},
		"ParticipantCaptureCoreV1": func(b []byte) ([]byte, error) {
			var v hitl.Participant
			if err := json.Unmarshal(b, &v); err != nil {
				return nil, err
			}
			return json.Marshal(v)
		},
		"HITLEnqueueRequestCoreV1": func(b []byte) ([]byte, error) {
			var v hitl.EnqueueRequest
			if err := json.Unmarshal(b, &v); err != nil {
				return nil, err
			}
			return json.Marshal(v)
		},
		"ResolvedTerminalOutcomeCoreV1": func(b []byte) ([]byte, error) {
			o, err := hitl.UnmarshalOutcome(b)
			if err != nil {
				return nil, err
			}
			return json.Marshal(o)
		},
	}
	skip := map[string]string{
		"handle-tangent-extras": "presentation fields are ignored on decode by design",
	}
	covered := 0
	for _, f := range hitltest.Fixtures() {
		dec, ok := decoders[f.Def]
		if !f.Valid || !ok {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			if why, s := skip[f.Name]; s {
				t.Skip(why)
			}
			out, err := dec(f.Doc)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !jsonEqual(t, f.Doc, out) {
				t.Fatalf("round trip changed the document\n in  %s\n out %s", f.Doc, out)
			}
			// What the Go types emit is itself valid under the same definition.
			v, err := schema.NewValidator(f.Def)
			if err != nil {
				t.Fatal(err)
			}
			if err := v.Validate(out); err != nil {
				t.Fatalf("re-marshaled document invalid: %v", err)
			}
		})
		covered++
	}
	if covered < 25 {
		t.Fatalf("only %d fixtures were round-tripped; the table is stale", covered)
	}
}

func TestTolerantOutputsIgnoreUnknownMembers(t *testing.T) {
	var h hitl.Handle
	if err := json.Unmarshal(fixture(t, "handle-tangent-extras"), &h); err != nil {
		t.Fatal(err)
	}
	if h.ItemID != "item_01" || h.State != hitl.StateResolved || h.Revision != 5 {
		t.Fatalf("handle = %+v", h)
	}
}

func TestOutcomeUnionRoundTripsAllFiveVariants(t *testing.T) {
	at := time.Date(2026, 9, 4, 3, 30, 0, 0, time.UTC)
	rev := int64(3)
	all := []hitl.Outcome{
		hitl.Resolved{ItemID: "i", InteractionRevision: 5, Resolution: hitl.ResolutionRecord{
			ResolutionID: "r", Response: hitl.Response{Kind: "approval", Decision: "approved", Note: "ok", Extra: map[string]json.RawMessage{"x": json.RawMessage(`1`)}},
			Participant: hitl.Participant{
				Responder: &hitl.Responder{Kind: "human", Ref: "u"}, Assurance: "authenticated",
				Proof: &hitl.Proof{Scheme: "webauthn", KeyRef: "cred", Binds: []hitl.ProofBind{{Name: "nonce", Value: "n"}}},
			},
			ResolvedAt: at, InteractionRevision: 5, PresentedProjectionRevision: &rev,
		}},
		hitl.Canceled{ItemID: "i", InteractionRevision: 3, Cause: hitl.CauseCallerWithdrawn, Reason: "why", TerminatedAt: at},
		hitl.Expired{ItemID: "i", InteractionRevision: 4, PolicyRef: hitl.ExpiryPolicyRef, TerminatedAt: at},
		hitl.Failed{ItemID: "i", InteractionRevision: 2, ErrorCode: "boom", Message: "it broke", TerminatedAt: at},
		hitl.Superseded{ItemID: "i", InteractionRevision: 3, ReplacementItemID: "j", TerminatedAt: at},
	}
	wantStates := []hitl.State{hitl.StateResolved, hitl.StateCanceled, hitl.StateExpired, hitl.StateFailed, hitl.StateSuperseded}
	def, err := schema.NewValidator("HITLTerminalOutcomeCoreV1")
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range all {
		if o.OutcomeState() != wantStates[i] || !o.OutcomeState().IsTerminal() {
			t.Errorf("variant %d state = %s", i, o.OutcomeState())
		}
		b, err := json.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		if verr := def.Validate(b); verr != nil {
			t.Fatalf("%s does not validate: %v\n%s", o.OutcomeState(), verr, b)
		}
		back, err := hitl.UnmarshalOutcome(b)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back, o) {
			t.Errorf("%s: round trip\n got  %#v\n want %#v", o.OutcomeState(), back, o)
		}
	}
}

func TestUnmarshalOutcomeRejects(t *testing.T) {
	for name, doc := range map[string]string{
		"nonterminal state": `{"state":"presented","item_id":"i","interaction_revision":1}`,
		"unknown state":     `{"state":"closed","item_id":"i","interaction_revision":1}`,
		"no item id":        `{"state":"expired","interaction_revision":1,"terminated_at":"2026-01-01T00:00:00Z"}`,
		"revision zero":     `{"state":"expired","item_id":"i","interaction_revision":0,"terminated_at":"2026-01-01T00:00:00Z"}`,
		"bad cause":         `{"state":"canceled","item_id":"i","interaction_revision":2,"cause":"policy","terminated_at":"2026-01-01T00:00:00Z"}`,
		"failed no message": `{"state":"failed","item_id":"i","interaction_revision":2,"error_code":"e","terminated_at":"2026-01-01T00:00:00Z"}`,
		"resolved bare":     `{"state":"resolved","item_id":"i","interaction_revision":2}`,
		"not json":          `nope`,
	} {
		if _, err := hitl.UnmarshalOutcome([]byte(doc)); !errors.Is(err, hitl.ErrInvalidOutcome) {
			t.Errorf("%s: err = %v, want ErrInvalidOutcome", name, err)
		}
	}
}

func TestItemViewRequiresOutcomeExactlyWhenTerminal(t *testing.T) {
	for _, name := range []string{"view-terminal-no-outcome", "view-nonterminal-with-outcome", "view-outcome-state-mismatch"} {
		var v hitl.ItemView
		f := fixture(t, name)
		if err := json.Unmarshal(f, &v); err == nil {
			t.Errorf("%s: decoded without error", name)
		}
	}
}

func TestDecodeErrorMapsWireCodesToTypedErrors(t *testing.T) {
	stale := hitl.DecodeError(fixture(t, "stale-withdraw-terminal"))
	var se *hitl.StaleRevisionError
	if !errors.As(stale, &se) || !errors.Is(stale, hitl.ErrStaleRevision) {
		t.Fatalf("stale = %v", stale)
	}
	if se.Operation != "withdraw" || se.Expected != 2 || se.Actual != 3 || se.CurrentState != hitl.StateCanceled || se.Outcome == nil || se.Outcome.OutcomeState() != hitl.StateCanceled {
		t.Fatalf("stale = %+v", se)
	}

	idem := hitl.DecodeError(fixture(t, "idempotency-conflict"))
	var ie *hitl.IdempotencyConflictError
	if !errors.As(idem, &ie) || !errors.Is(idem, hitl.ErrIdempotencyConflict) || ie.ExistingItemID != "item_01" || ie.IdempotencyKey != "k-1" {
		t.Fatalf("idempotency = %v", idem)
	}

	term := hitl.DecodeError(fixture(t, "terminal-conflict"))
	var te *hitl.TerminalConflictError
	if !errors.As(term, &te) || !errors.Is(term, hitl.ErrTerminalConflict) || te.Outcome.OutcomeState() != hitl.StateResolved {
		t.Fatalf("terminal = %v", term)
	}

	nf := hitl.DecodeError(fixture(t, "error-plain-not-found"))
	if !errors.Is(nf, hitl.ErrNotFound) {
		t.Fatalf("not_found = %v", nf)
	}
	var ce *hitl.CodedError
	if !errors.As(hitl.DecodeError([]byte(`{"code":"await_timeout","message":"m"}`)), &ce) || ce.Code != "await_timeout" {
		t.Fatalf("coded = %v", ce)
	}
	for _, bad := range []string{`nope`, `{}`, `{"code":"terminal_conflict"}`, `{"code":"stale_revision","terminal_outcome":{"state":"presented"}}`} {
		if err := hitl.DecodeError([]byte(bad)); err == nil {
			t.Errorf("DecodeError(%s) = nil", bad)
		}
	}
}

func TestEncodeErrorProducesSchemaValidBodiesThatDecodeBack(t *testing.T) {
	at := time.Date(2026, 9, 4, 3, 30, 0, 0, time.UTC)
	out := hitl.Canceled{ItemID: "i", InteractionRevision: 3, Cause: hitl.CauseCallerWithdrawn, TerminatedAt: at}
	cases := []struct {
		err error
		def string
		is  error
	}{
		{&hitl.StaleRevisionError{Operation: "withdraw", ItemID: "i", RevisionKind: "interaction", Expected: 1, Actual: 3, CurrentState: hitl.StateCanceled, Outcome: out}, "HITLErrorV1", hitl.ErrStaleRevision},
		{&hitl.IdempotencyConflictError{IdempotencyKey: "k", ExistingItemID: "i"}, "HITLErrorV1", hitl.ErrIdempotencyConflict},
		{&hitl.TerminalConflictError{Outcome: out}, "HITLErrorV1", hitl.ErrTerminalConflict},
		{hitl.ErrNotFound, "HITLPlainErrorV1", hitl.ErrNotFound},
		{hitl.NewInvalidRequest(errors.New("bad")), "HITLPlainErrorV1", hitl.ErrInvalidRequest},
		{errors.New("something else"), "HITLPlainErrorV1", nil},
	}
	for _, c := range cases {
		body := hitl.EncodeError(c.err)
		v, err := schema.NewValidator(c.def)
		if err != nil {
			t.Fatal(err)
		}
		if err := v.Validate(body); err != nil {
			t.Errorf("%v: body invalid under %s: %v\n%s", c.err, c.def, err, body)
		}
		back := hitl.DecodeError(body)
		if c.is != nil && !errors.Is(back, c.is) {
			t.Errorf("%v: decoded to %v, want Is(%v)", c.err, back, c.is)
		}
	}
}

func TestCommandsDecodeStrictly(t *testing.T) {
	if _, err := hitl.DecodeGetCommand([]byte(`{"contract_version":"1.0","item_id":"i","caller":{"application_id":"a"}}`)); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"unknown member":    `{"contract_version":"1.0","item_id":"i","caller":{"application_id":"a"},"x":1}`,
		"unknown in caller": `{"contract_version":"1.0","item_id":"i","caller":{"application_id":"a","x":1}}`,
		"wrong version":     `{"contract_version":"2.0","item_id":"i","caller":{"application_id":"a"}}`,
		"no caller":         `{"contract_version":"1.0","item_id":"i"}`,
		"trailing data":     `{"contract_version":"1.0","item_id":"i","caller":{"application_id":"a"}} {}`,
	} {
		if _, err := hitl.DecodeGetCommand([]byte(doc)); !errors.Is(err, hitl.ErrInvalidRequest) {
			t.Errorf("get %s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	base := `{"contract_version":"1.0","item_id":"i","caller":{"application_id":"a"},"wait_ms":%s}`
	for wait, ok := range map[string]bool{"0": true, "30000": true, "50000": true, "-1": false, "50001": false, "1.5": false} {
		_, err := hitl.DecodeAwaitCommand([]byte(strings.Replace(base, "%s", wait, 1)))
		if (err == nil) != ok {
			t.Errorf("await wait_ms=%s: err = %v, want ok=%v", wait, err, ok)
		}
	}
	c, err := hitl.DecodeAwaitCommand([]byte(`{"contract_version":"1.0","item_id":"i","caller":{"application_id":"a"}}`))
	if err != nil || c.Wait() != 30*time.Second {
		t.Fatalf("default wait = %v, %v; want 30s", c.Wait(), err)
	}
	for name, doc := range map[string]string{
		"zero revision": `{"contract_version":"1.0","item_id":"i","caller":{"application_id":"a"},"expected_revision":0}`,
		"blank reason":  `{"contract_version":"1.0","item_id":"i","caller":{"application_id":"a"},"reason":"  "}`,
		"extra member":  `{"contract_version":"1.0","item_id":"i","caller":{"application_id":"a"},"cause":"x"}`,
	} {
		if _, err := hitl.DecodeWithdrawCommand([]byte(doc)); !errors.Is(err, hitl.ErrInvalidRequest) {
			t.Errorf("withdraw %s: err = %v", name, err)
		}
	}
}

func TestExtrasSurviveResponseAndEnqueueRoundTrips(t *testing.T) {
	doc := fixture(t, "enqueue-tangent-full")
	var r hitl.EnqueueRequest
	if err := json.Unmarshal(doc, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Extra) != 4 { // title summary request evidence
		t.Fatalf("Extra = %v", r.Extra)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(t, doc, b) {
		t.Fatalf("round trip\n in  %s\n out %s", doc, b)
	}
}

func TestProofSlotIsOptionalAndOpen(t *testing.T) {
	plain := hitl.Participant{Responder: &hitl.Responder{Kind: "agent", Ref: "a1"}, Assurance: "asserted"}
	if err := plain.Validate(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(plain)
	if strings.Contains(string(b), "proof") {
		t.Fatalf("absent proof must not be emitted: %s", b)
	}
	for _, scheme := range []string{"webauthn", "x509", "some-future-scheme"} {
		p := plain
		p.Proof = &hitl.Proof{Scheme: scheme, KeyRef: "k", Binds: []hitl.ProofBind{}}
		if err := p.Validate(); err != nil {
			t.Errorf("scheme %q: %v", scheme, err)
		}
	}
	bad := plain
	bad.Proof = &hitl.Proof{Scheme: "webauthn", KeyRef: ""}
	if err := bad.Validate(); !errors.Is(err, hitl.ErrInvalidRequest) {
		t.Errorf("proof without key_ref: %v", err)
	}
}
