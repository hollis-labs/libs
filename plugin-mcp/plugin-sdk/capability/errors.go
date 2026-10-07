package capability

// Code is a stable application failure classification.
type Code string

const (
	InvalidRequest        Code = "invalid_request"
	Unauthenticated       Code = "unauthenticated"
	CapabilityDenied      Code = "capability_denied"
	ScopeDenied           Code = "scope_denied"
	UnsupportedCapability Code = "unsupported_capability"
	TargetUnavailable     Code = "target_unavailable"
	BudgetExceeded        Code = "budget_exceeded"
	RateLimited           Code = "rate_limited"
	Cancelled             Code = "cancelled"
	DeadlineExceeded      Code = "deadline_exceeded"
	UnknownOutcome        Code = "unknown_outcome"
	InternalError         Code = "internal_error"
	Conflict              Code = "conflict"
)

// EffectState distinguishes authorization refusal from an ambiguous mutation.
type EffectState string

const (
	NotStarted   EffectState = "not_started"
	NotCommitted EffectState = "not_committed"
	Committed    EffectState = "committed"
	Unknown      EffectState = "unknown"
)

// Error contains only safe classification metadata. Adapters must not copy
// internal error messages, arguments or credentials into wire errors.
type Error struct {
	Code        Code          `json:"code"`
	Capability  string        `json:"capability,omitempty"`
	RequestID   RequestID     `json:"request_id,omitempty"`
	EffectState EffectState   `json:"effect_state"`
	Detail      FailureDetail `json:"detail,omitempty"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Capability }

// RPCErrorData is the protocol-2 host application error payload. All errors
// prohibit automatic retry, including unknown outcomes and idempotent hints.
type RPCErrorData struct {
	Contract    string        `json:"contract"`
	Code        Code          `json:"code"`
	RequestID   RequestID     `json:"request_id"`
	EffectState EffectState   `json:"effect_state"`
	Retryable   bool          `json:"retryable"`
	Detail      FailureDetail `json:"detail,omitempty"`
}

const HostRPCErrorCode = -32010

// FailureDetail is a closed machine-readable refinement, never raw diagnostics.
type FailureDetail string

const (
	StaleBinding   FailureDetail = "stale_binding"
	CallbackCycle  FailureDetail = "callback_cycle"
	DepthExceeded  FailureDetail = "depth_exceeded"
	ParentInvalid  FailureDetail = "parent_invalid"
	ParentTerminal FailureDetail = "parent_terminal"
)

// Validate checks classification metadata before RPC correlation is attached.
// Zero RequestID is allowed for internal planning/admission errors; RPCData
// requires a positive ID before serialization. Missing state is unknown.
func (e *Error) Validate() error {
	if e == nil {
		return refusal(InvalidRequest, "")
	}
	state := e.EffectState
	if state == "" {
		state = Unknown
	}
	switch state {
	case NotStarted, NotCommitted, Committed, Unknown:
	default:
		return refusal(InvalidRequest, e.Capability)
	}
	switch e.Code {
	case InvalidRequest, Unauthenticated, CapabilityDenied, ScopeDenied, UnsupportedCapability, TargetUnavailable, BudgetExceeded, RateLimited, Cancelled, DeadlineExceeded, UnknownOutcome, InternalError, Conflict:
	default:
		return refusal(InvalidRequest, e.Capability)
	}
	if e.Code == UnknownOutcome && state != Unknown {
		return refusal(InvalidRequest, e.Capability)
	}
	switch e.Detail {
	case "", StaleBinding, CallbackCycle, DepthExceeded, ParentInvalid, ParentTerminal:
	default:
		return refusal(InvalidRequest, e.Capability)
	}
	if e.RequestID != 0 && e.RequestID.Validate() != nil {
		return refusal(InvalidRequest, e.Capability)
	}
	return nil
}

// RPCData validates classification and the positive profile correlation ID.
// Adapters must copy the enclosing request ID; equality with that envelope is
// checked by the transport, which owns the envelope.
func (e *Error) RPCData() (RPCErrorData, error) {
	if err := e.Validate(); err != nil {
		return RPCErrorData{}, err
	}
	if err := e.RequestID.Validate(); err != nil {
		return RPCErrorData{}, err
	}
	state := e.EffectState
	if state == "" {
		state = Unknown
	}
	return RPCErrorData{Contract: "host-rpc/1", Code: e.Code, RequestID: e.RequestID, EffectState: state, Retryable: false, Detail: e.Detail}, nil
}

func refusal(code Code, name string) *Error {
	return &Error{Code: code, Capability: name, EffectState: NotStarted}
}
