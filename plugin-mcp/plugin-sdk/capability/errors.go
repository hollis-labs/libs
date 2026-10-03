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
	Code        Code        `json:"code"`
	Capability  string      `json:"capability,omitempty"`
	RequestID   string      `json:"request_id,omitempty"`
	EffectState EffectState `json:"effect_state"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Capability }

// RPCErrorData is the protocol-2 host application error payload. All errors
// prohibit automatic retry, including unknown outcomes and idempotent hints.
type RPCErrorData struct {
	Contract    string      `json:"contract"`
	Code        Code        `json:"code"`
	RequestID   string      `json:"request_id"`
	EffectState EffectState `json:"effect_state"`
	Retryable   bool        `json:"retryable"`
}

const HostRPCErrorCode = -32010

func (e *Error) RPCData() RPCErrorData {
	return RPCErrorData{"host-rpc/1", e.Code, e.RequestID, e.EffectState, false}
}
func refusal(code Code, name string) *Error {
	return &Error{Code: code, Capability: name, EffectState: NotStarted}
}
